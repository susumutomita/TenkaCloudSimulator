import sonarjs from 'eslint-plugin-sonarjs';
import tseslint from 'typescript-eslint';

// Keep typed rules in a named block so their migration scope is explicit.
const typedRules = {
  '@typescript-eslint/no-floating-promises': 'warn',
  '@typescript-eslint/no-misused-promises': 'warn',
  '@typescript-eslint/await-thenable': 'error',
  '@typescript-eslint/no-base-to-string': 'error',
};

export default tseslint.config(
  {
    ignores: [
      '**/dist/**',
      '**/build/**',
      '**/coverage/**',
      '**/node_modules/**',
      '**/*.spec.ts',
      '**/*.test.ts',
    ],
  },
  ...tseslint.configs.strict,
  ...tseslint.configs.stylistic,
  sonarjs.configs.recommended,
  {
    files: ['**/*.{ts,tsx,mts,cts}'],
    rules: {
      '@typescript-eslint/consistent-type-assertions': [
        'error',
        { assertionStyle: 'never' },
      ],
      '@typescript-eslint/no-explicit-any': 'error',
      '@typescript-eslint/no-non-null-assertion': 'error',
      '@typescript-eslint/ban-ts-comment': 'error',
      // no-floating-promises requires `void promise` for intentional fire-and-forget.
      // Sonar's rule forbids that exact construct, so the promise-safety rule wins.
      'sonarjs/void-use': 'off',
    },
  },
  {
    files: ['scripts/**/*.ts'],
    languageOptions: {
      parser: tseslint.parser,
      parserOptions: {
        project: ['./tsconfig.scripts.json'],
        tsconfigRootDir: import.meta.dirname,
      },
    },
    rules: typedRules,
  }
);
