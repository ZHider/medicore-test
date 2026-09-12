package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// "仍在占用号源"的判断是好几处逻辑的共同前提：
// 预约能不能写病历、能不能做预问诊、超时释放要不要回补号源。
//
// 单独把它抽出来（IsActiveAppointmentStatus）就是为了让这些地方
// 走**同一个**判断；这个用例把"哪些状态算占用"钉死，
// 将来加状态（比如"改期中"）时如果漏改，这里会红。
func TestIsActiveAppointmentStatus(t *testing.T) {
	active := []string{AppointmentStatusPending, AppointmentStatusConfirmed}
	inactive := []string{
		AppointmentStatusCancelled,
		AppointmentStatusExpired,
		"",        // 空值：宁可当作"不占用"，也不要当成有效
		"unknown", // 未知状态：同上
	}

	for _, s := range active {
		assert.True(t, IsActiveAppointmentStatus(s), "%q 应视为仍占用号源", s)
	}
	for _, s := range inactive {
		assert.False(t, IsActiveAppointmentStatus(s), "%q 不应视为占用号源", s)
	}
}

// (*Appointment).IsActive 必须与状态字符串版本保持一致。
//
// 两者曾经是分开写的两份条件，正是"加个新状态就漏改一处"的温床。
func TestAppointmentIsActive_MatchesStatusHelper(t *testing.T) {
	for _, s := range []string{
		AppointmentStatusPending, AppointmentStatusConfirmed,
		AppointmentStatusCancelled, AppointmentStatusExpired,
	} {
		appt := &Appointment{Status: s}
		assert.Equal(t, IsActiveAppointmentStatus(s), appt.IsActive(),
			"状态 %q 在两种写法下结论必须一致", s)
	}
}

// 终态判断决定 worker 还会不会重投这条任务。
//
// 判错的后果：把已完成当未完成 → 反复总结同一份报告；
// 把失败当未完成 → 死信队列里的任务被无限重试。
func TestAIReportIsTerminal(t *testing.T) {
	terminal := []string{AIReportStatusDone, AIReportStatusFailed}
	nonTerminal := []string{AIReportStatusPending, AIReportStatusProcessing, "", "unknown"}

	for _, s := range terminal {
		r := &AIReport{Status: s}
		assert.True(t, r.IsTerminal(), "%q 应是终态", s)
	}
	for _, s := range nonTerminal {
		r := &AIReport{Status: s}
		assert.False(t, r.IsTerminal(), "%q 不应是终态", s)
	}
}
