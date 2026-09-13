import MockAdapter from 'axios-mock-adapter'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import * as adminApi from './admin'
import * as appointmentApi from './appointment'
import * as authApi from './auth'
import * as catalogApi from './catalog'
import { instance } from './client'
import * as preconsultApi from './preconsult'
import * as recordApi from './record'

/**
 * API 层的**契约测试**：逐条钉死每个函数发出的请求形状（方法 / 路径 / 查询参数 / 请求体）。
 *
 * 为什么值得单独一层：
 *   - 后端契约是冻结的（29 个端点，被 60 个集成测试守护），前端唯一能出错的地方
 *     就是**把路径或参数写错**。这类错误在浏览器里表现为 404 或"筛选没生效"，
 *     而单测能在 1 秒内指出是哪一个函数的哪一个参数。
 *   - 顺带覆盖 `cleanParams`：首页不该带 `cursor=`，否则后端会收到空串游标。
 *
 * 与 `client.spec.ts` 的分工：那边测拦截器**逻辑**（刷新/去重/重试），
 * 这边测 api/*.ts 拼出来的**报文**。两者用不同的 mock 手段：
 * 这里用 axios-mock-adapter 在 adapter 层拦截，因此真实的拦截器链会跑一遍。
 */
const mock = new MockAdapter(instance)

beforeEach(() => {
  mock.reset()
  // 所有未显式声明的请求都返回成功信封，避免用例只关心 URL 时还要写桩
  mock.onAny().reply((config) => {
    const body = config.data ? JSON.parse(String(config.data)) : undefined
    // 回显请求，方便断言"发出去的到底是什么"
    return [200, { code: 0, message: 'ok', data: { echo: { url: config.url, params: config.params, body } }, trace_id: 't' }]
  })
})

afterEach(() => {
  mock.reset()
})

/** 取最近一次请求。 */
const lastGet = () => mock.history.get[mock.history.get.length - 1]!
const lastPost = () => mock.history.post[mock.history.post.length - 1]!

describe('auth —— 路径与请求体', () => {
  it('register 走 POST /auth/register', async () => {
    await authApi.register({ phone: '13800000000', name: '张三', password: 'Passw0rd!' })
    expect(lastPost().url).toBe('/auth/register')
    expect(JSON.parse(lastPost().data as string)).toEqual({
      phone: '13800000000',
      name: '张三',
      password: 'Passw0rd!',
    })
  })

  it('login 走 POST /auth/login', async () => {
    await authApi.login({ phone: '13900000001', password: 'Doctor@123' })
    expect(lastPost().url).toBe('/auth/login')
  })

  it('me 走 GET /auth/me', async () => {
    await authApi.me()
    expect(lastGet().url).toBe('/auth/me')
  })

  it('logout **必须把 refresh_token 放进 body**（否则服务端那份不会被撤销）', async () => {
    await authApi.logout('refresh-abc')
    expect(lastPost().url).toBe('/auth/logout')
    expect(JSON.parse(lastPost().data as string)).toEqual({ refresh_token: 'refresh-abc' })
  })
})

describe('catalog —— 路径与必填查询参数', () => {
  it('listDepartments 走 GET /departments', async () => {
    await catalogApi.listDepartments()
    expect(lastGet().url).toBe('/departments')
  })

  it('listDoctors 走 GET /departments/:id/doctors', async () => {
    await catalogApi.listDoctors(3)
    expect(lastGet().url).toBe('/departments/3/doctors')
  })

  it('listSchedulesByDepartment **始终带上 date**（不传后端会按空日期查）', async () => {
    await catalogApi.listSchedulesByDepartment(1, { date: '2026-09-20' })
    expect(lastGet().url).toBe('/departments/1/schedules')
    expect(lastGet().params).toMatchObject({ date: '2026-09-20' })
  })

  it('listSchedulesByDepartment 首页**不带 cursor**', async () => {
    await catalogApi.listSchedulesByDepartment(1, { date: '2026-09-20', size: 20 })
    // cursor 为 undefined 时必须被 cleanParams 丢掉，否则会拼出 ?cursor=
    expect(lastGet().params).not.toHaveProperty('cursor')
    expect(lastGet().params).toMatchObject({ date: '2026-09-20', size: 20 })
  })

  it('listSchedulesByDepartment 翻页时带上 cursor', async () => {
    await catalogApi.listSchedulesByDepartment(1, { date: '2026-09-20', cursor: 88 })
    expect(lastGet().params).toMatchObject({ cursor: 88 })
  })

  it('getDoctor 走 GET /doctors/:id', async () => {
    await catalogApi.getDoctor(3)
    expect(lastGet().url).toBe('/doctors/3')
  })

  it('listSchedulesByDoctor 带上 from 与 to', async () => {
    await catalogApi.listSchedulesByDoctor(3, { from: '2026-09-13', to: '2026-09-26' })
    expect(lastGet().url).toBe('/doctors/3/schedules')
    expect(lastGet().params).toMatchObject({ from: '2026-09-13', to: '2026-09-26' })
  })
})

describe('appointment —— 路径与安全参数', () => {
  it('grab 走 POST /appointments 并带上幂等键', async () => {
    await appointmentApi.grab({ schedule_id: 88, idempotency_key: 'k-12345678' })
    expect(lastPost().url).toBe('/appointments')
    expect(JSON.parse(lastPost().data as string)).toEqual({
      schedule_id: 88,
      idempotency_key: 'k-12345678',
    })
  })

  it('listMine 首页不带 cursor', async () => {
    await appointmentApi.listMine({ size: 20 })
    expect(lastGet().url).toBe('/appointments')
    expect(lastGet().params).not.toHaveProperty('cursor')
  })

  it('cancel 不带原因时 body 是空对象（不是 {reason: undefined}）', async () => {
    await appointmentApi.cancel(501)
    expect(lastPost().url).toBe('/appointments/501/cancel')
    expect(JSON.parse(lastPost().data as string)).toEqual({})
  })

  it('cancel 带原因时原样发送', async () => {
    await appointmentApi.cancel(501, '临时有事')
    expect(JSON.parse(lastPost().data as string)).toEqual({ reason: '临时有事' })
  })

  it('checkIn 走 POST /appointments/:id/checkin', async () => {
    await appointmentApi.checkIn(501)
    expect(lastPost().url).toBe('/appointments/501/checkin')
  })

  it('listForDoctor 的 status 为空串时**不发送该参数**（后端会 400）', async () => {
    await appointmentApi.listForDoctor({ date: '2026-09-20', status: '' })
    expect(lastGet().url).toBe('/doctor/appointments')
    expect(lastGet().params).not.toHaveProperty('status')
    expect(lastGet().params).toMatchObject({ date: '2026-09-20' })
  })

  it('listForDoctor 带 status 时原样发送白名单值', async () => {
    await appointmentApi.listForDoctor({ status: 'confirmed' })
    expect(lastGet().params).toMatchObject({ status: 'confirmed' })
  })

  it('getQueue 走 GET /schedules/:id/queue', async () => {
    await appointmentApi.getQueue(14)
    expect(lastGet().url).toBe('/schedules/14/queue')
  })
})

describe('preconsult —— 路径', () => {
  it('history / submit / report 三个路径', async () => {
    await preconsultApi.getHistory(501)
    expect(lastGet().url).toBe('/appointments/501/preconsult/history')

    await preconsultApi.submit(501)
    expect(lastPost().url).toBe('/appointments/501/preconsult/submit')

    await preconsultApi.getReport(501)
    expect(lastGet().url).toBe('/appointments/501/preconsult/report')
  })
})

describe('record —— 路径与请求体', () => {
  it('writeRecord 走 POST /appointments/:id/record', async () => {
    await recordApi.writeRecord(501, { diagnosis: '急性上呼吸道感染' })
    expect(lastPost().url).toBe('/appointments/501/record')
    expect(JSON.parse(lastPost().data as string)).toEqual({ diagnosis: '急性上呼吸道感染' })
  })

  it('getRecord 走 GET /appointments/:id/record', async () => {
    await recordApi.getRecord(501)
    expect(lastGet().url).toBe('/appointments/501/record')
  })

  it('listRecords 走 GET /records（身份由后端按令牌分流，前端不传角色）', async () => {
    await recordApi.listRecords({ size: 20 })
    expect(lastGet().url).toBe('/records')
    expect(lastGet().params).not.toHaveProperty('role')
  })
})

describe('admin —— 路径与筛选参数', () => {
  it('listAuditLogs 走 GET /admin/audit-logs 并透传筛选', async () => {
    await adminApi.listAuditLogs({
      resource: 'appointments',
      action: 'create',
      from: '2026-09-01',
      to: '2026-09-30',
      user_id: 12,
      size: 20,
    })
    expect(lastGet().url).toBe('/admin/audit-logs')
    expect(lastGet().params).toMatchObject({
      resource: 'appointments',
      action: 'create',
      from: '2026-09-01',
      to: '2026-09-30',
      user_id: 12,
    })
  })

  // 追踪号查询是"用户截图报错 → 定位到那条请求"的入口。
  // 参数名必须与后端一致：写成 traceId 不会报错，只会静默地返回全部日志。
  it('listAuditLogs 透传 trace_id 且参数名是下划线形式', async () => {
    const traceId = 'ee33b8045a4cfb7a6f0ba98062ab25c6'
    await adminApi.listAuditLogs({ trace_id: traceId })

    expect(lastGet().params).toMatchObject({ trace_id: traceId })
    expect(lastGet().params).not.toHaveProperty('traceId')
  })

  it('listAuditLogs 空筛选项全部被丢掉', async () => {
    await adminApi.listAuditLogs({})
    const params = lastGet().params ?? {}
    for (const key of ['resource', 'action', 'trace_id', 'from', 'to', 'user_id', 'cursor']) {
      expect(params).not.toHaveProperty(key)
    }
  })

  // 界面上把追踪号留空时必须**不传**该参数，而不是传一个空串：
  // 后端对空串的语义是"不限"，但传空串会让 URL 里多出一个无意义的 trace_id=。
  it('listAuditLogs 空 trace_id 不被带上', async () => {
    await adminApi.listAuditLogs({ trace_id: '' })
    expect(lastGet().params ?? {}).not.toHaveProperty('trace_id')
  })

  it('triggerSync 走 POST /admin/sync/:source', async () => {
    await adminApi.triggerSync('mockhis')
    expect(lastPost().url).toBe('/admin/sync/mockhis')
  })

  it('triggerSync 对 source 做 URL 编码（避免拼出非法路径）', async () => {
    await adminApi.triggerSync('a/b')
    expect(lastPost().url).toBe('/admin/sync/a%2Fb')
  })

  it('listSyncLogs 走 GET /admin/sync-logs', async () => {
    await adminApi.listSyncLogs({ source: 'mockhis' })
    expect(lastGet().url).toBe('/admin/sync-logs')
    expect(lastGet().params).toMatchObject({ source: 'mockhis' })
  })
})

describe('请求头', () => {
  it('注入 Content-Type: application/json', async () => {
    await catalogApi.listDepartments()
    // axios 会把 headers 归一化成 AxiosHeaders，用 toJSON 取普通对象
    const headers = lastGet().headers as unknown as { toJSON?: () => Record<string, unknown> }
    const plain = headers?.toJSON ? headers.toJSON() : (headers as Record<string, unknown>)
    expect(plain['Content-Type']).toBe('application/json')
  })
})
