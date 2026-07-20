// Language selector UI components
import {
  SUPPORTED_LANGS,
  getLanguageName,
  t,
} from "../lib/i18n.js";

// Create admin language settings panel
export function createAdminLanguageSettings(defaultLanguage) {
  return `
    <div class="card">
      <div class="card-head">
        <h2>${t("admin.settings")}</h2>
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
        <h2>${t("settings.language")}</h2>
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

