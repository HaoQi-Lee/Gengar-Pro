package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"time"
)

type ClientConfig struct {
	APIKey      string
	BaseURL     string
	Model       string
	Temperature *float64 // optional; nil uses Claude default
}

type Client struct {
	cfg  ClientConfig
	http *http.Client
}

// DefaultMaxTokens 单次响应输出上限。4096 会把巡检报告类长输出掐断
// （stop_reason=max_tokens，曾表现为内容静默缺尾）；16384 为当前全部
// 在售 Claude 模型接受的值（当前代上限 64K-128K）。
const DefaultMaxTokens = 16384

// streamReadIdleTimeout 是流式请求的读空闲看门狗：SSE 超过该时长无任何
// 帧到达即判定链路死亡。取代整请求 120s Timeout——后者会把 >2 分钟的
// 长报告（合法慢输出）连着成功一半的流一起杀掉（评审 ②-2 勘误：
// 「流式无超时顾虑」的旧注释不成立，http.Client.Timeout 覆盖整个
// 请求生命周期，包括流式 body 的读取）。
const streamReadIdleTimeout = 90 * time.Second

// retryAttempts / retryBaseDelay 建流失败的重试参数（429/5xx/网络类）。
// 测试可整包替换 sleep 与 rand 以消除等待。
const (
	retryAttempts = 3
	// retryBaseDelay 基础退避；实际等待 = base << attempt 加 ±30% 抖动，
	// 防多实例同步重试形成风暴。
	retryBaseDelay = 100 * time.Millisecond
)

func NewClient(cfg ClientConfig) *Client {
	return &Client{
		cfg: cfg,
		http: &http.Client{
			Timeout: 120 * time.Second, // 非流式整请求超时
		},
	}
}

// streamHTTP 返回流式专用 client：无整体 Timeout（长输出合法慢），
// 读空闲由 streamReadIdleTimeout watchdog 在 parseSSE 层掐断。拨号与
// TLS 握手仍受限制——无 Timeout 的 client 拨号可能挂很久。
func (c *Client) streamHTTP() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second, // 等首字节（含服务端排队）
		},
	}
}

// SendMessage 非流式请求（当前仅在测试/非 agent 路径使用）。429/5xx/
// 网络类错误重试 3 次指数退避+抖动；400/401 等客户端错误不重试。
func (c *Client) SendMessage(ctx context.Context, system string, messages []Message, tools []Tool) (*APIResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	reqBody := apiRequest{
		Model:       c.cfg.Model,
		MaxTokens:   DefaultMaxTokens,
		Temperature: c.cfg.Temperature,
		System:      system,
		Messages:    messages,
		Tools:       tools,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	var resp *APIResponse
	err = retryRetryable(ctx, func() error {
		r, rerr := c.doRequest(ctx, body)
		if rerr != nil {
			return rerr
		}
		resp = r
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("after retries: %w", err)
	}
	return resp, nil
}

// retryRetryable 执行 fn 至多 retryAttempts 次：不可重试错误（400/401
// 等客户端错误）立即返回；可重试错误（429/5xx/网络）指数退避 + ±30%
// 抖动后重试。抖动防止多实例同步重试形成请求风暴。
func retryRetryable(ctx context.Context, fn func() error) error {
	var lastErr error
	for attempt := 0; attempt < retryAttempts; attempt++ {
		if attempt > 0 {
			d := retryBaseDelay << (attempt - 1)
			jitter := time.Duration(float64(d) * (0.7 + 0.6*rand.Float64()))
			select {
			case <-time.After(jitter):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		err := fn()
		if err == nil {
			return nil
		}
		lastErr = err
		if !isRetryable(err) {
			return err
		}
	}
	return lastErr
}

// isRetryable 判定错误是否值得重试：网络层错误与 429/5xx 可重试，
// 4xx 客户端错误（鉴权/请求格式）重试无意义。
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	if _, ok := err.(net.Error); ok {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "rate limited") ||
		strings.Contains(msg, "502") || strings.Contains(msg, "503") ||
		strings.Contains(msg, "504") || strings.Contains(msg, "500") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "context deadline")
}

func (c *Client) doRequest(ctx context.Context, body []byte) (*APIResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.cfg.BaseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.cfg.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	httpResp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if httpResp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("rate limited (429)")
	}
	if httpResp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error %d: %s", httpResp.StatusCode, string(respBody))
	}

	var apiResp APIResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	return &apiResp, nil
}

// TruncateOutput truncates output exceeding maxLen characters.
func TruncateOutput(output string, maxLen, headLen int) string {
	if len(output) <= maxLen {
		return output
	}
	head := output[:headLen]
	tail := output[len(output)-headLen:]
	omitted := len(output) - 2*headLen
	return head + fmt.Sprintf("\n\n... truncated %d characters ...\n\n", omitted) + tail
}
