// Package mask 提供接口输出脱敏（spec §2.2：DTO 层统一脱敏）。
package mask

// Phone 脱敏手机号：13812345678 -> 138****5678。
func Phone(phone string) string {
	r := []rune(phone)
	if len(r) < 7 {
		// 过短无法保留有效的头尾，整体打码避免泄露。
		return repeat('*', len(r))
	}
	return string(r[:3]) + "****" + string(r[len(r)-4:])
}

// IDCard 脱敏身份证：110101199001011234 -> 110101********1234。
//
// 保留头 6 位（行政区划）与尾 4 位，中间全部打码 —— 中间恰好要留**至少一位**。
// 因此长度不足 11 时必须整体打码：曾经的判断是 len < 10，
// 于是恰好 10 位时 repeat('*', 0) 产生空串，函数**原样返回输入**，
// 脱敏完全失效（由 fuzz 测试 FuzzMaskNeverLeaksOriginal 发现）。
//
// 当前不可达（注册接口 binding 强制 len=18，且 UserProfile 不返回身份证），
// 但这是一个"名字叫脱敏、实际会原样返回"的导出函数，留着迟早被误用。
func IDCard(id string) string {
	r := []rune(id)
	if len(r) < 11 {
		return repeat('*', len(r))
	}
	return string(r[:6]) + repeat('*', len(r)-10) + string(r[len(r)-4:])
}

// Name 脱敏姓名：张三 -> 张*；欧阳修 -> 欧**。
func Name(name string) string {
	r := []rune(name)
	if len(r) <= 1 {
		return string(r)
	}
	return string(r[:1]) + repeat('*', len(r)-1)
}

func repeat(ch rune, n int) string {
	if n <= 0 {
		return ""
	}
	out := make([]rune, n)
	for i := range out {
		out[i] = ch
	}
	return string(out)
}
