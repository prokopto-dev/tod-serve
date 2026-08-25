// The console's lint configuration.
//
// The load-bearing block is the last one: `tod/no-network-outside-api` is ON everywhere under
// `src/`, and `src/api/**` is the one directory where it is off. That inversion is deliberate —
// the rule is a default, and the exemption is a named place, so a new directory is covered without
// anybody remembering to add it.

import js from '@eslint/js'
import reactHooks from 'eslint-plugin-react-hooks'
import globals from 'globals'
import tseslint from 'typescript-eslint'

import tod from './eslint-rules/no-network-outside-api.js'

export default tseslint.config(
  { ignores: ['dist/**', 'node_modules/**'] },

  js.configs.recommended,
  ...tseslint.configs.recommended,

  // The build scripts run in Node, not a browser. They are linted — a typo in the generator is a
  // client that does not compile — but against the right globals.
  {
    files: ['scripts/**/*.mjs', 'eslint-rules/**/*.js', '*.config.js'],
    languageOptions: { ecmaVersion: 2023, sourceType: 'module', globals: globals.node },
  },

  {
    files: ['**/*.{ts,tsx}'],
    languageOptions: {
      ecmaVersion: 2023,
      globals: globals.browser,
      parserOptions: { ecmaFeatures: { jsx: true } },
    },
    plugins: { 'react-hooks': reactHooks },
    rules: {
      ...reactHooks.configs.recommended.rules,
      '@typescript-eslint/no-unused-vars': ['error', { argsIgnorePattern: '^_' }],
      // `any` in a domain signature is banned in Go here and it is banned here for the same
      // reason: a type that admits everything is a type that checks nothing, and the whole point
      // of generating the client from the document is that a renamed field is a compile error.
      '@typescript-eslint/no-explicit-any': 'error',
      eqeqeq: ['error', 'always', { null: 'ignore' }],
    },
  },

  // AGENTS.md law 7. See eslint-rules/no-network-outside-api.js for why it is a rule AND a grep.
  {
    files: ['src/**/*.{ts,tsx}'],
    plugins: { tod },
    rules: { 'tod/no-network-outside-api': 'error' },
  },
  {
    files: ['src/api/**/*.{ts,tsx}'],
    rules: { 'tod/no-network-outside-api': 'off' },
  },

  // The generated client is not hand-written and is not reviewed line by line. Linting it would
  // mean editing a generated file to satisfy a style rule, which is how a generated file stops
  // being generated.
  { ignores: ['src/api/generated.ts'] },
)
