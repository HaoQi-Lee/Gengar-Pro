package host

// Resolve 把目标引用列表解析为具体主机。每个引用依次匹配：
//  1. 精确主机名（优先级最高）
//  2. 特殊值 "all" —— 清单内全部主机
//  3. 标签 —— 带该标签的全部主机
//
// 无法解析的引用收集在第二个返回值里（不报错中断，让调用方决定
// 是回传模型还是直接失败）。结果按命中顺序去重。
func Resolve(hosts []*Host, refs []string) (matched []*Host, unresolved []string) {
	byName := make(map[string]*Host, len(hosts))
	for _, h := range hosts {
		byName[h.Name] = h
	}
	seen := make(map[string]bool)
	for _, ref := range refs {
		var hits []*Host
		if h, ok := byName[ref]; ok {
			hits = []*Host{h}
		} else if ref == "all" {
			hits = hosts
		} else {
			for _, h := range hosts {
				if h.HasTag(ref) {
					hits = append(hits, h)
				}
			}
		}
		if len(hits) == 0 {
			unresolved = append(unresolved, ref)
			continue
		}
		for _, h := range hits {
			if !seen[h.ID] {
				seen[h.ID] = true
				matched = append(matched, h)
			}
		}
	}
	return matched, unresolved
}
