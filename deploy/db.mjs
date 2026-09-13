#!/usr/bin/env node
/**
 * 调试用的数据库 / Redis 客户端入口。
 *
 * 两套编排的口令与端口**完全不同**，手敲容易连错库（这是最坑的一种调试事故：
 * 查了半天发现查的是另一个数据库）。所以本工具自动识别当前该连哪一套：
 *
 *   stack (全栈编排)  medicore-mysql / medicore-redis
 *                     无宿主机端口，靠 docker exec；口令在 deploy/.env（随机）
 *   dev   (开发依赖)  medicore-dev-mysql / medicore-dev-redis
 *                     映射到 127.0.0.1:3306 / 6379；口令固定 medicore123
 *
 * 用 --stack / --dev 可以强制指定。
 *
 * 用法：
 *   node deploy/db.mjs mysql                 # 进 MySQL 交互式 shell
 *   node deploy/db.mjs mysql "SELECT ..."    # 执行单条 SQL
 *   node deploy/db.mjs redis                 # 进 redis-cli
 *   node deploy/db.mjs redis "KEYS stock:*"  # 执行单条命令
 *   node deploy/db.mjs dump [文件]           # 导出 SQL（默认 deploy/backup.sql）
 *   node deploy/db.mjs restore <文件>        # 导入 SQL
 *   node deploy/db.mjs url                   # 打印连接信息（给 GUI 客户端用）
 *   node deploy/db.mjs port 13306            # 起临时端口转发容器（Ctrl+C 结束）
 */

import { spawnSync } from 'node:child_process'
import { existsSync, readFileSync, createWriteStream } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const deployDir = dirname(fileURLToPath(import.meta.url))
const envPath = resolve(deployDir, '.env')

/** 两套编排的连接参数。口令是"怎么拿到"，不是明文，避免写死在源码里。 */
const PROFILES = {
  stack: {
    label: '全栈编排 (medicore)',
    mysqlContainer: 'medicore-mysql',
    redisContainer: 'medicore-redis',
    network: 'medicore',
    hostPort: null,
    user: 'medicore',
    database: 'medicore',
    // 口令来自 deploy/.env
    password: (env) => {
      if (!existsSync(envPath)) {
        fatal('deploy/.env 不存在。请先运行：nub run stack:init')
      }
      const values = {}
      for (const line of readFileSync(envPath, 'utf8').split('\n')) {
        const match = /^([A-Z0-9_]+)=(.*)$/.exec(line.trim())
        if (match) values[match[1]] = match[2]
      }
      // 注意是 STACK_MYSQL_PASSWORD，不是 MEDICORE_MYSQL_PASSWORD：
      // 后者是"容器内给后端读"的键，**根 .env 里也有同名的一份**（开发用）。
      // deploy/.env 统一用 STACK_ 前缀以避免两套配置撞名，详见 compose.yml 顶部说明。
      const password = values.STACK_MYSQL_PASSWORD
      if (!password) fatal('deploy/.env 里没有 STACK_MYSQL_PASSWORD')
      return password
    },
  },
  dev: {
    label: '开发依赖 (medicore-dev)',
    mysqlContainer: 'medicore-dev-mysql',
    redisContainer: 'medicore-dev-redis',
    network: 'medicore-dev_default',
    hostPort: 3306,
    user: 'medicore',
    database: 'medicore',
    // 开发编排用的是 compose.dev.yml 里写死的口令（本来就明文在仓库里）
    password: () => 'medicore123',
  },
}

function fatal(message) {
  console.error(`错误: ${message}`)
  process.exit(1)
}

function containerExists(name) {
  const result = spawnSync('docker', ['inspect', '--format', '{{.State.Status}}', name], {
    encoding: 'utf8',
  })
  return result.status === 0 ? result.stdout.trim() : null
}

/**
 * 选择要连的编排。
 *
 * 默认策略：两套都跑着时**优先 stack**（它是"正式"的那套）；
 * 只有一套在跑就用那套；都没跑则报错并提示怎么起。
 * 两套都跑却不说明时给出显式提示，避免"以为连的是 A 其实连的是 B"。
 */
function pickProfile(forced) {
  if (forced) {
    if (!PROFILES[forced]) fatal(`未知的 profile：${forced}（可选 stack / dev）`)
    const state = containerExists(PROFILES[forced].mysqlContainer)
    if (!state) {
      fatal(
        `${PROFILES[forced].label} 没有在运行（找不到容器 ${PROFILES[forced].mysqlContainer}）。\n` +
          (forced === 'stack' ? '  启动：nub run stack:up' : '  启动：nub run deps:up'),
      )
    }
    return { name: forced, ...PROFILES[forced] }
  }

  const up = Object.entries(PROFILES).filter(
    ([, p]) => containerExists(p.mysqlContainer) !== null,
  )

  if (up.length === 0) {
    fatal(
      '没有找到任何在运行的 MySQL 容器。\n' +
        '  起开发依赖（本机 go run 调试用）：nub run deps:up\n' +
        '  起全栈编排（演示用）：            nub run stack:up',
    )
  }

  if (up.length === 2) {
    console.error(
      '提示：stack 与 dev 两套编排都在运行，本次默认连 **stack**（全栈编排）。\n' +
        '      要连开发那套请显式指定：node deploy/db.mjs --dev mysql\n',
    )
  }

  const name = up.find(([n]) => n === 'stack')?.[0] ?? up[0][0]
  return { name, ...PROFILES[name] }
}

// 解析 --stack / --dev（可出现在任意位置）
const rawArgs = process.argv.slice(2)
let forced = null
const args = []
for (const arg of rawArgs) {
  if (arg === '--stack') forced = 'stack'
  else if (arg === '--dev') forced = 'dev'
  else args.push(arg)
}

const [command, ...rest] = args
const profile = pickProfile(forced)
const password = profile.password()
const isDev = profile.name === 'dev'

function run(cmd, cmdArgs, opts = {}) {
  const result = spawnSync(cmd, cmdArgs, { stdio: 'inherit', ...opts })
  if (result.error) fatal(`无法执行 ${cmd}: ${result.error.message}`)
  process.exit(result.status ?? 1)
}

/** 需要拿到输出内容（而非透传）时用这个。 */
function capture(cmd, args) {
  const result = spawnSync(cmd, args, { encoding: 'utf8' })
  if (result.error) fatal(`无法执行 ${cmd}: ${result.error.message}`)
  return result
}

switch (command) {
  case 'mysql': {
    const sql = rest.join('')
    // dev 那套映射了宿主机端口，但仍优先走 docker exec ——
    // 这样两条路径行为一致，也不需要宿主机装 mysql 客户端。
    const args = [
      'exec',
      ...(sql ? [] : ['-i']), // 交互式需要 -i；单条查询不需要
      profile.mysqlContainer,
      'mysql',
      '--default-character-set=utf8mb4', // 不加会在 Windows 控制台显示成 ??
      `-u${profile.user}`,
      `-p${password}`,
      profile.database,
      ...(sql ? ['-e', sql] : []),
    ]
    run('docker', args)
    break
  }

  case 'redis': {
    const cmd = rest.join(' ')
    run('docker', [
      'exec',
      ...(cmd ? [] : ['-i']),
      profile.redisContainer,
      'redis-cli',
      ...(cmd ? cmd.split(' ') : []),
    ])
    break
  }

  case 'dump': {
    const out = rest[0] ?? resolve(deployDir, 'backup.sql')
    // 在容器内用容器自己的环境变量拿口令，口令不出现在宿主机命令行
    const dumpCmd = isDev
      ? `exec mysqldump -u${profile.user} -p"${password}" --single-transaction --routines --databases ${profile.database}`
      : 'exec mysqldump -u' +
        profile.user +
        ' -p"$MYSQL_PASSWORD" --single-transaction --routines medicore'
    const result = spawnSync(
      'docker',
      ['exec', profile.mysqlContainer, 'sh', '-c', dumpCmd],
      { encoding: 'buffer', maxBuffer: 512 * 1024 * 1024 },
    )
    if (result.status !== 0) {
      fatal(`mysqldump 失败：${result.stderr?.toString() ?? ''}`)
    }
    const stream = createWriteStream(out)
    stream.write(result.stdout)
    stream.end()
    console.log(`已导出 ${result.stdout.length} 字节 -> ${out}（来源：${profile.label}）`)
    break
  }

  case 'restore': {
    const file = rest[0]
    if (!file) fatal('用法：node deploy/db.mjs restore <文件>')
    if (!existsSync(file)) fatal(`文件不存在：${file}`)
    // 用 shell 重定向把文件喂给容器内的 mysql
    run('sh', [
      '-c',
      `docker exec -i ${profile.mysqlContainer} sh -c 'exec mysql -u${profile.user} -p"${password}" ${profile.database}' < "${file}"`,
    ])
    break
  }

  case 'url': {
    console.log(`当前连接：${profile.label}\n`)
    console.log('MySQL')
    console.log(`  user     ${profile.user}`)
    console.log(`  database ${profile.database}`)
    if (profile.hostPort) {
      console.log(`  host     127.0.0.1:${profile.hostPort}   ← 已映射到宿主机，可直接连`)
    } else {
      console.log(`  host     无宿主机端口；容器 ${profile.mysqlContainer} 内为 mysql:3306`)
    }
    console.log(`  口令     ${isDev ? password : '见 deploy/.env 的 STACK_MYSQL_PASSWORD'}`)
    console.log('')
    console.log('Redis')
    console.log(
      profile.hostPort
        ? `  addr     127.0.0.1:6379`
        : `  addr     容器 ${profile.redisContainer} 内为 redis:6379`,
    )
    if (!profile.hostPort) {
      console.log('')
      console.log('要用 GUI 客户端，先起端口转发：')
      console.log('  node deploy/db.mjs port 13306     然后连 127.0.0.1:13306')
    }
    break
  }

  case 'port': {
    if (profile.hostPort) {
      fatal(
        `${profile.label} 已经把 MySQL 映射到 127.0.0.1:${profile.hostPort}，不需要端口转发。\n` +
          '  直接连即可（口令见 node deploy/db.mjs url）。',
      )
    }
    const hostPort = rest[0] ?? '13306'
    console.log(`正在把 ${profile.network} 网络的 mysql:3306 转发到 127.0.0.1:${hostPort}`)
    console.log(`现在可以用 Navicat / DBeaver 连接 127.0.0.1:${hostPort}（user=${profile.user}）`)
    console.log('按 Ctrl+C 结束转发。\n')
    run('docker', [
      'run',
      '--rm',
      '-it',
      '--network',
      profile.network,
      '-p',
      `${hostPort}:3306`,
      'alpine/socat',
      'tcp-listen:3306,fork,reuseaddr',
      'tcp-connect:mysql:3306',
    ])
    break
  }

  default:
    console.log(`MediCore 调试工具

当前连接：${profile.label}

用法：
  node deploy/db.mjs mysql [SQL]     进 MySQL shell，或执行一条 SQL
  node deploy/db.mjs redis [命令]    进 redis-cli，或执行一条命令
  node deploy/db.mjs dump [文件]     导出 SQL（默认 deploy/backup.sql）
  node deploy/db.mjs restore <文件>  导入 SQL
  node deploy/db.mjs url             打印连接信息
  node deploy/db.mjs port [端口]     起临时端口转发（给 GUI 客户端用）

选择连哪一套（默认自动识别，两套都在跑时优先 stack）：
  --stack    全栈编排 medicore-mysql（无宿主机端口，口令在 deploy/.env）
  --dev      开发依赖 medicore-dev-mysql（127.0.0.1:3306，口令 medicore123）

提示：
  - 数据卷为空时才执行 docs/schema.sql；改了表结构要重启对应的编排并清卷
  - stack 那套改了 deploy/.env 的口令后必须 stack:reset，否则会 Access denied
`)
    break
}
