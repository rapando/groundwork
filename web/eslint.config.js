import js from '@eslint/js'
import pluginVue from 'eslint-plugin-vue'
import tseslint from 'typescript-eslint'
import prettier from 'eslint-config-prettier'
import globals from 'globals'

// Correctness only: formatting is Prettier's job (TypeScript files) or the
// author's (Vue templates are kept compact on purpose).
export default tseslint.config(
  { ignores: ['node_modules', 'dist', 'playwright-report', 'test-results', 'e2e/screens'] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  ...pluginVue.configs['flat/essential'],
  { files: ['**/*.vue'], languageOptions: { parserOptions: { parser: tseslint.parser } } },
  { languageOptions: { globals: { ...globals.browser, ...globals.node } } },
  {
    rules: {
      '@typescript-eslint/no-explicit-any': 'off', // API payloads are typed where it pays; events are not
      '@typescript-eslint/no-unused-vars': ['error', { argsIgnorePattern: '^_', caughtErrors: 'none' }],
      'vue/multi-word-component-names': 'off',
      'no-empty': ['error', { allowEmptyCatch: true }],
    },
  },
  prettier,
)
