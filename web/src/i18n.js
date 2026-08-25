// Reactive bridge over the framework-independent lib/i18n dictionary. tt()
// touches store.lang so any component rendering it re-renders when the
// language switches; setLanguage performs the switch and bumps the counter.

import { ref } from "vue";
import elementEN from "element-plus/es/locale/lang/en";
import elementZH from "element-plus/es/locale/lang/zh-cn";
import { t as rawT, switchLanguage as rawSwitch, getCurrentLanguage, LANG_CHINESE } from "@/lib/i18n.js";
import { store } from "@/store";

// Element's own strings (select placeholders, ElMessageBox buttons, table empty
// text, date pickers) come from its locale pack, not from lib/i18n — without
// this they stay English while the rest of the console is Chinese. The ref
// feeds App.vue's <el-config-provider>, which applies it live.
export const elementLocale = ref(elementEN);

export function applyElementLocale() {
  elementLocale.value = getCurrentLanguage() === LANG_CHINESE ? elementZH : elementEN;
}

export function tt(key, paramsOrDefault = null, params = null) {
  void store.lang; // reactive dependency for templates
  return rawT(key, paramsOrDefault, params);
}

export async function setLanguage(lang) {
  await rawSwitch(lang);
  applyElementLocale();
  store.lang++;
}
