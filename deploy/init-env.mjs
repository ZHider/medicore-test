#!/usr/bin/env node
/**
 * 生成 deploy/.env —— 全栈编排所需的环境变量。
 *
 * 为什么要单独一个脚本：
 *   - 仓库里**不含任何密钥**（项目硬规则）。后端启动时 config.Validate()
 *     会拒绝空密钥，所以起栈前必须先有这三个值。
 *   - 密钥必须**稳定**：aes_key 一变，库里已加密的手机号/身份证全部无法解密，
 *     phone_hash 也不再匹配，现有账号直接登不上。因此本脚本默认**拒绝覆盖**。
 *   - 生成必须是**密码学随机**。PowerShell 的 Get-Random 不是 CSPRNG，
 *     用它生成 jwt_secret 等于给自己埋一个可预测的签名密钥。
 *     node:crypto 的 randomBytes 是 CSPRNG，且不需要引入任何新依赖。
 *
 * 用法：
 *   node deploy/init-env.mjs            # 不存在则生成，存在则拒绝
 *   node deploy/init-env.mjs --force    # 覆盖（会换掉 aes_key，见上面的警告）
 *   node deploy/init-env.mjs --print    # 只打印当前配置（含管理员口令）
 */

import { execFileSync } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import { chmodSync, existsSync, readFileSync, writeFileSync } from 'node:fs'
import { createServer, createConnection } from 'node:net'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const deployDir = dirname(fileURLToPath(import.meta.url))
const envPath = join(deployDir, '.env')

const args = process.argv.slice(2)
const force = args.includes('--force')
const printOnly = args.includes('--print')

// ---------------------------------------------------------------------------
// 与后端 internal/config/validate.go 保持一致的强度判据
//
// 这里刻意复刻一份而不是放宽：若生成的值会被后端拒绝，启动会在
// "invalid config" 处失败，而那个报错不会告诉你"是生成器写了坏值"。
// 复刻成本很低，能把这个失败提前到生成阶段。
// ---------------------------------------------------------------------------

const PLACEHOLDER_HINTS = [
  'change-me',
  'changeme',
  'placeholder',
  'example',
  'your-',
  'yoursecret',
  'todo',
  'dummy',
  'sample',
]

function isSingleRepeatedChar(s) {
  for (let i = 1; i < s.length; i += 1) if (s[i] !== s[0]) return false
  return s.length > 0
}

function hasRepeatingPeriod(s) {
  const n = s.length
  for (let period = 1; period <= n / 2; period += 1) {
    if (n % period !== 0) continue
    let repeats = true
    for (let i = period; i < n; i += 1) {
      if (s[i] !== s[i % period]) {
        repeats = false
        break
      }
    }
    if (repeats) return true
  }
  return false
}

function containsAscendingRun(s, minRun) {
  let run = 1
  for (let i = 1; i < s.length; i += 1) {
    if (s.charCodeAt(i) === s.charCodeAt(i - 1) + 1) {
      run += 1
      if (run >= minRun) return true
      continue
    }
    run = 1
  }
  return false
}

function looksPredictable(s) {
  const lower = s.toLowerCase()
  for (const hint of PLACEHOLDER_HINTS) {
    if (lower.includes(hint)) return `包含占位词 "${hint}"`
  }
  if (isSingleRepeatedChar(s)) return '由单一字符重复构成'
  if (hasRepeatingPeriod(s)) return '由短周期重复构成'
  if (containsAscendingRun(s, 8)) return '包含连续升序序列'
  return ''
}

/** 生成一个既能通过后端校验、又是密码学随机的十六进制密钥。 */
function secretHex(bytes) {
  for (let attempt = 0; attempt < 16; attempt += 1) {
    const value = randomBytes(bytes).toString('hex')
    if (!looksPredictable(value)) return value
  }
  // 十六进制随机串撞上"短周期/升序"的概率极低，真撞上说明随机源有问题
  throw new Error('无法生成满足后端强度校验的密钥（随机源异常？）')
}

// ---------------------------------------------------------------------------
// 环境探测
// ---------------------------------------------------------------------------

function detectGoModCache() {
  try {
    const out = execFileSync('go', ['env', 'GOMODCACHE'], {
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'ignore'],
    }).trim()
    // .env 里反斜杠可能被当作转义，统一成正斜杠（Windows 也认）
    return out.replace(/\\/g, '/')
  } catch {
    return ''
  }
}

/**
 * 判断端口是否可被 Docker 发布。
 *
 * 必须用**两种**探测，只做一种会误判：
 *   - 只做 bind：Windows 允许在别的进程已绑 127.0.0.1:80 的情况下
 *     再绑 0.0.0.0:80，于是"绑定成功"并不代表端口可用。
 *   - 只做 connect：反向代理可能只监听 IPv6，connect 到 IPv4 会失败。
 * 实测教训：本机 80 被 caddy 占用，而只做 bind 探测时它报告"空闲"。
 */
async function isPortFree(port) {
  // 1) 有东西能连上 -> 端口在用
  const occupied = await new Promise((resolve) => {
    const socket = createConnection({ host: '127.0.0.1', port, timeout: 800 })
    socket.once('connect', () => {
      socket.destroy()
      resolve(true)
    })
    socket.once('timeout', () => {
      socket.destroy()
      resolve(false)
    })
    socket.once('error', () => resolve(false))
  })
  if (occupied) return false

  // 2) 0.0.0.0 也能绑 -> Docker 发布端口时用的就是这个
  return new Promise((resolve) => {
    const server = createServer()
    server.once('error', () => resolve(false))
    server.once('listening', () => server.close(() => resolve(true)))
    server.listen(port, '0.0.0.0')
  })
}

/**
 * 选宿主端口。
 *
 * spec §11.2 要求 frontend 暴露 80 端口 —— 那是**容器**端口，固定为 80。
 * 宿主机侧端口可配。
 *
 * ⚠️ 候选里**刻意避开 8080**：那是本机 `go run` 后端的端口
 * （`nub run backend:dev` / `nub run dev`），而编排与本机开发
 * 是可能同时运行的两种拓扑 —— 撞上会让其中一个起不来，
 * 且报错是 "port is already allocated"，与项目本身毫无关联。
 */
async function pickHostPort() {
  if (await isPortFree(80)) return 80
  for (const candidate of [8090, 8000, 8088, 9080]) {
    // eslint-disable-next-line no-await-in-loop
    if (await isPortFree(candidate)) return candidate
  }
  return 8090
}

// ---------------------------------------------------------------------------
// 主流程
// ---------------------------------------------------------------------------

function readExisting() {
  if (!existsSync(envPath)) return null
  const text = readFileSync(envPath, 'utf8')
  const values = {}
  for (const line of text.split('\n')) {
    const match = /^([A-Z0-9_]+)=(.*)$/.exec(line.trim())
    if (match) values[match[1]] = match[2]
  }
  return { text, values }
}

function render(values) {
  return `# 由 deploy/init-env.mjs 生成 —— 请勿提交（已被 .gitignore 排除）。
#
# ⚠️ 重新生成会换掉 STACK_AES_KEY，后果：
#      - 数据库中已加密的手机号 / 身份证无法解密
#      - phone_hash 不匹配，现有账号无法登录
#      - 已签发的所有令牌失效
#    确实要重做：node deploy/init-env.mjs --force（并重置数据库卷）

# ---- 网络 ----
# 容器内固定监听 80；这里是宿主机侧映射端口
STACK_HTTP_PORT=${values.STACK_HTTP_PORT}
# 时区：影响 seed 生成的排班日期与所有 DATETIME 的本地语义
STACK_TZ=${values.STACK_TZ}

# ---- 数据库 ----
STACK_MYSQL_PASSWORD=${values.STACK_MYSQL_PASSWORD}
STACK_MYSQL_ROOT_PASSWORD=${values.STACK_MYSQL_ROOT_PASSWORD}

# ---- 应用安全密钥（缺一不可，后端起不来）----
# aes_key      泄露 -> 全部病历与手机号明文泄露
# phone_pepper 泄露 -> 可用彩虹表反推手机号
# jwt_secret   泄露 -> 可伪造任意用户（含管理员）的令牌
STACK_AES_KEY=${values.STACK_AES_KEY}
STACK_PHONE_PEPPER=${values.STACK_PHONE_PEPPER}
STACK_JWT_SECRET=${values.STACK_JWT_SECRET}

# ---- 演示数据 ----
# cmd/seed 建管理员账号时用它；不设置则回退到 Admin@12345 并打印警告
STACK_ADMIN_PASSWORD=${values.STACK_ADMIN_PASSWORD}

# ---- 构建 ----
# 后端镜像用宿主机的 Go 模块缓存做**离线**编译（GOPROXY=off）。
# 留空会让 compose 构建直接报错并提示重新运行 stack:init。
GOMODCACHE=${values.GOMODCACHE}
`
}

async function main() {
  const existing = readExisting()

  if (printOnly) {
    if (!existing) {
      console.error('deploy/.env 不存在，请先运行：node deploy/init-env.mjs')
      process.exit(1)
    }
    printSummary(existing.values, true)
    return
  }

  if (existing && !force) {
    console.error(
      `deploy/.env 已存在，未覆盖。\n\n` +
        `重新生成会换掉 aes_key，导致：\n` +
        `  - 数据库中已加密的手机号 / 身份证无法解密\n` +
        `  - phone_hash 不匹配，现有账号无法登录\n` +
        `  - 已签发的所有令牌失效\n\n` +
        `要查看当前配置：node deploy/init-env.mjs --print\n` +
        `确认要重做：  node deploy/init-env.mjs --force\n`,
    )
    process.exit(1)
  }

  const goModCache = detectGoModCache()
  const hostPort = await pickHostPort()
  const tz = Intl.DateTimeFormat().resolvedOptions().timeZone || 'Asia/Shanghai'

  const values = {
    STACK_HTTP_PORT: String(hostPort),
    STACK_TZ: tz,
    STACK_MYSQL_PASSWORD: secretHex(16),
    STACK_MYSQL_ROOT_PASSWORD: secretHex(16),
    // 64 字节 hex = 128 字符，远超后端要求的 64/16/32，留足余量
    STACK_AES_KEY: secretHex(32),
    STACK_PHONE_PEPPER: secretHex(32),
    STACK_JWT_SECRET: secretHex(32),
    STACK_ADMIN_PASSWORD: `Adm-${randomBytes(9).toString('base64url')}`,
    GOMODCACHE: goModCache,
  }

  writeFileSync(envPath, render(values), { encoding: 'utf8', mode: 0o600 })
  try {
    chmodSync(envPath, 0o600)
  } catch {
    // Windows 上 chmod 基本无效，忽略
  }

  console.log(`已生成 ${envPath}\n`)
  printSummary(values, false)

  if (!goModCache) {
    console.warn(
      '\n⚠️  未能探测到 GOMODCACHE（go 不在 PATH 上？）。\n' +
        '   后端镜像将退化为容器内联网下载依赖。若不需要离线构建可忽略。',
    )
  }
}

function printSummary(values, includeSecrets) {
  const mask = (v) => (includeSecrets ? v : `${'*'.repeat(8)}（已写入 .env）`)
  console.log('  访问地址        http://localhost:' + values.STACK_HTTP_PORT)
  console.log('  时区            ' + values.STACK_TZ)
  console.log('  管理员账号      13800000000')
  console.log('  管理员口令      ' + values.STACK_ADMIN_PASSWORD)
  console.log('  演示医生        13900000001 … 13900000012 / Doctor@123')
  console.log('  aes_key         ' + mask(values.STACK_AES_KEY))
  console.log('  gomodcache      ' + (values.GOMODCACHE || '(未探测到)'))
}

main().catch((error) => {
  console.error(`错误: ${error.message}`)
  process.exit(1)
})
