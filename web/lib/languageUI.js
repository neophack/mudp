// Language selector UI components
import {
  SUPPORTED_LANGS,
  getLanguageName,
  t,
} from "../lib/i18n.js";

// Globe icon reused on both language cards so they read as one family.
const langIcon =
  `<svg class="card-head-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">` +
  `<circle cx="12" cy="12" r="10"/><path d="M12 2a14.5 14.5 0 0 0 0 20 14.5 14.5 0 0 0 0-20"/><path d="M2 12h20"/>` +
  `</svg>`;

// Create admin language settings panel
export function createAdminLanguageSettings(defaultLanguage) {
  return `
    <div class="card">
      <div class="card-head">
        <h2>${langIcon}${t("settings.defaultLanguage")}</h2>
        <span class="card-head-sub">${t("settings.defaultLanguageSub")}</span>
      </div>
      <div class="card-body">
        <form id="adminLanguageForm" class="compact">
          <p class="hint">${t("admin.userCanOverride")}</p>
          <label for="defaultLanguageSelect">${t("admin.defaultLanguage")}:</label>
          <select id="defaultLanguageSelect" name="defaultLanguage" required>
            ${SUPPORTED_LANGS.map(
              (lang) =>
                `<option value="${lang}" ${lang === defaultLanguage ? "selected" : ""}>${getLanguageName(lang)}</option>`
            ).join("")}
          </select>
          <p class="hint">${t("admin.newUsersWillUse")}</p>
          <button type="submit">${t("common.save")}</button>
        </form>
      </div>
    </div>
  `;
}

// Create user language settings panel
export function createUserLanguageSettings(userLanguage) {
  return `
    <div class="card">
      <div class="card-head">
        <h2>${langIcon}${t("settings.language")}</h2>
        <span class="card-head-sub">${t("settings.languageSub")}</span>
      </div>
      <div class="card-body">
        <form id="userLanguageForm" class="compact">
          <label for="userLanguageSelect">${t("settings.currentLanguage")}:</label>
          <select id="userLanguageSelect" name="language" required>
            ${SUPPORTED_LANGS.map(
              (lang) =>
                `<option value="${lang}" ${lang === userLanguage ? "selected" : ""}>${getLanguageName(lang)}</option>`
            ).join("")}
          </select>
          <button type="submit">${t("common.save")}</button>
        </form>
      </div>
    </div>
  `;
}
