/** 前端内部的 UI 类型（非后端契约）。 */

import type { Role } from './api'

export interface NavItem {
  label: string
  /** 路由名 */
  name: string
  /** AppIcon 的图标键 */
  icon: string
  /** 缺省表示任意已登录角色可见 */
  roles?: Role[]
}
