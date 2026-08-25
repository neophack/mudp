import js from "@eslint/js";
import pluginVue from "eslint-plugin-vue";
import globals from "globals";

export default [
  {
    ignores: ["vendor/**", "wasm/**", "node_modules/**", "dist/**", "coverage/**"],
  },
  js.configs.recommended,
  // Vue 3 SFCs (src/**/*.vue): template parsing + the essential error rules.
  ...pluginVue.configs["flat/essential"],
  {
    files: ["src/**/*.vue"],
    rules: {
      // Route views are registered by name in the router; single-word names
      // ("Netdisk", "Users") are deliberate and collide with no HTML tag we use.
      "vue/multi-word-component-names": "off",
      // The few v-html spots render DOMPurify-sanitized markdown/terminal HTML.
      "vue/no-v-html": "off",
      // Template formatting is enforced by review, not lint — these pure-style
      // rules produce thousands of warnings over Element UI attribute lists.
      "vue/max-attributes-per-line": "off",
      "vue/singleline-html-element-content-newline": "off",
      "vue/multiline-html-element-content-newline": "off",
      "vue/html-self-closing": "off",
      "vue/html-closing-bracket-spacing": "off",
      "vue/attributes-order": "off",
      "vue/order-in-components": "off",
    },
  },
  {
    languageOptions: {
      ecmaVersion: 2024,
      sourceType: "module",
      globals: {
        ...globals.browser,
        ...globals.node,
        globalThis: "readonly",
      },
    },
    rules: {
      "no-unused-vars": [
        "error",
        { argsIgnorePattern: "^_", varsIgnorePattern: "^_", caughtErrorsIgnorePattern: "^_" },
      ],
      "no-undef": "error",
      "no-redeclare": "error",
      "no-unreachable": "error",
      "no-empty": ["error", { allowEmptyCatch: true }],
      "no-case-declarations": "off",
      "no-cond-assign": ["error", "except-parens"],
      "no-fallthrough": "error",
      "getter-return": "error",
      "no-self-assign": "error",
      "no-console": "off",
    },
  },
  {
    files: ["tests/**/*.js"],
    languageOptions: {
      globals: {
        ...globals.browser,
        ...globals.node,
        describe: "readonly",
        it: "readonly",
        expect: "readonly",
        beforeEach: "readonly",
        afterEach: "readonly",
        beforeAll: "readonly",
        afterAll: "readonly",
        test: "readonly",
        vi: "readonly",
      },
    },
  },
];
