import js from '@eslint/js'
import pluginVue from 'eslint-plugin-vue'
import tseslint from 'typescript-eslint'

export default tseslint.config(
  // dist 必须忽略：压缩产物是单行超长文件，会被 lint 出数百条假错误
  { ignores: ['dist/**', 'node_modules/**', 'public/**'] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  ...pluginVue.configs['flat/recommended'],
  {
    files: ['**/*.vue'],
    languageOptions: {
      parserOptions: { parser: tseslint.parser },
    },
    rules: {
      // .vue 的 <script> 走 TS 解析器，未定义标识符由 TypeScript 精确报错
      // （tsc 认识 lib.dom 的全部全局类型），eslint 的 no-undef 在这里只会
      // 对 ResizeObserver / HTMLDivElement 这类**类型位置**的全局误报。
      'no-undef': 'off',
      // 可选性已经由 TS 的 `?` 与 `??` 回退逻辑表达；再要求 withDefaults 里的
      // 空字符串/undefined 默认值属于风格偏好，且会让"可选"变成"有值但为空"，
      // 反而模糊了语义（例如 StatusTag 的 label 需要区分"未传"与"传了空串"）。
      'vue/require-default-prop': 'off',
    },
  },
)
