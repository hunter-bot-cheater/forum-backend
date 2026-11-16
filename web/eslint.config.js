// ESLint 扁平配置（ESLint 9 起只认这种格式）。
//
// 设计原则与后端 .golangci.yml 一致：只开能指出真问题的规则。
// 一条规则如果长期靠 eslint-disable 压制，它就是在制造噪音，应该关掉。
//
// 不引 prettier：工程已经用「2 空格、无分号、单引号」稳定写了几十个文件，
// 再挂一个格式化工具只会让编辑器保存时批量改写 diff，掩盖真正的改动。
import js from '@eslint/js'
import tseslint from 'typescript-eslint'
import pluginVue from 'eslint-plugin-vue'

export default tseslint.config(
  {
    // 构建产物与依赖不参与检查：dist 里是压缩过的代码，报错也无法修。
    ignores: ['dist/**', 'node_modules/**', 'coverage/**'],
  },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  ...pluginVue.configs['flat/essential'],
  {
    // .vue 的 <script lang="ts"> 要交给 TS 解析器，否则 import 与类型注解都会被当成语法错误。
    files: ['**/*.vue'],
    languageOptions: {
      parserOptions: {
        parser: tseslint.parser,
      },
    },
  },
  {
    rules: {
      // 项目里确有需要绕过类型的地方（请求封装、Element 实例引用）。
      // 逐处写 any 是权衡后的结果，强制 unknown 会把代码改成一片类型体操。
      '@typescript-eslint/no-explicit-any': 'off',
      // catch {} 里不写变量是有意的：拦截器已经统一弹过提示，
      // 页面只需要「失败就停在这里」，把 error 绑出来再不用才是噪音。
      '@typescript-eslint/no-unused-vars': ['error', { caughtErrors: 'none' }],
      // 单文件组件名允许单词（Article.vue、Login.vue 都在 views 下按目录归类）。
      'vue/multi-word-component-names': 'off',
    },
  },
  {
    // Vue 官方的 .vue 类型垫片就是这么写的：DefineComponent<{}, {}, any>
    // 表示「没有 props、没有 setup 返回」，改成 object 反而与 DefineComponent 的签名对不上。
    // 这类文件是模板化的声明，不是业务代码，规则放行。
    files: ['**/*.d.ts'],
    rules: {
      '@typescript-eslint/no-empty-object-type': 'off',
    },
  },
)
