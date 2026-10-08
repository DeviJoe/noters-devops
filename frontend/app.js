"use strict";

const $ = (id) => document.getElementById(id);
const state = { notes: [], total: 0, query: "", request: null, editing: null, snapshot: "", saving: false, timer: null, user: null, authMode: "login", authBusy: false, registrationEnabled: false, draft: null };
const pageSize = 24;
const dateFormat = new Intl.DateTimeFormat("ru-RU", { day: "numeric", month: "short", year: "numeric" });

async function api(path, options = {}) {
  const response = await fetch(path, { credentials: "same-origin", ...options, headers: { "Content-Type": "application/json", "X-Noters-CSRF": "1", ...options.headers } });
  if (response.status === 204) return null;
  let body;
  try { body = await response.json(); } catch {
    const error = new Error(response.status === 429 ? "Слишком много попыток входа. Подождите минуту." : "Сервис временно недоступен. Попробуйте ещё раз.");
    error.status = response.status;
    throw error;
  }
  if (!response.ok) {
    const error = new Error(body.error || "Не удалось выполнить запрос.");
    error.status = response.status;
    if (response.status === 401 && path.startsWith("/api/notes")) showAuth("Сессия истекла. Войдите снова, чтобы продолжить.");
    throw error;
  }
  return body;
}

function plural(count) {
  const n = count % 100;
  if (n >= 11 && n <= 14) return "заметок";
  return count % 10 === 1 ? "заметка" : count % 10 >= 2 && count % 10 <= 4 ? "заметки" : "заметок";
}

function element(tag, className, text) {
  const node = document.createElement(tag);
  node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

function renderNotes() {
  const fragment = document.createDocumentFragment();
  for (const [index, note] of state.notes.entries()) {
    const card = element("button", `note-card${note.pinned ? " pinned" : ""}`);
    card.type = "button";
    card.setAttribute("aria-label", `Открыть заметку: ${note.title}`);
    const top = element("div", "card-top");
    top.append(element("span", "card-index", `ЗАМЕТКА ${String(index + 1).padStart(2, "0")}`));
    if (note.pinned) top.append(element("span", "pin-badge", "Закреплена"));
    const bottom = element("div", "card-bottom");
    bottom.append(element("span", "", dateFormat.format(new Date(note.updated_at))), element("span", "card-arrow", "↗"));
    card.append(top, element("h2", "", note.title), element("p", "note-preview", note.content.slice(0, 400) || "Без текста"), bottom);
    card.addEventListener("click", () => openEditor(note));
    fragment.append(card);
  }
  $("notes").replaceChildren(fragment);
  $("note-count").textContent = `${state.query ? "Найдено: " : ""}${state.total} ${plural(state.total)}`;
  $("empty-state").hidden = state.total !== 0;
  $("empty-title").textContent = state.query ? "Ничего не нашлось" : "Всё начинается с первой заметки";
  $("empty-text").textContent = state.query ? "Попробуйте другое слово или очистите поиск." : "Запишите идею, составьте план или сохраните то, что важно.";
  $("empty-create").hidden = Boolean(state.query);
  $("load-more").hidden = state.notes.length >= state.total;
}

async function loadNotes(append = false) {
  if (!state.user) return;
  state.request?.abort();
  const request = new AbortController();
  state.request = request;
  $("notes").setAttribute("aria-busy", "true");
  $("load-more").disabled = true;
  $("error-banner").hidden = true;
  $("empty-state").hidden = true;
  $("note-count").textContent = "Загрузка…";
  const params = new URLSearchParams({ q: state.query, limit: pageSize, offset: append ? state.notes.length : 0 });
  try {
    const data = await api(`/api/notes?${params}`, { signal: request.signal });
    if (state.request !== request) return;
    state.notes = append ? [...state.notes, ...data.notes] : data.notes;
    state.total = data.total;
    renderNotes();
  } catch (error) {
    if (error.name === "AbortError" || state.request !== request) return;
    $("error-text").textContent = error.message;
    $("error-banner").hidden = false;
    $("note-count").textContent = "Не удалось загрузить заметки";
    $("load-more").hidden = true;
  } finally {
    if (state.request === request) {
      $("notes").setAttribute("aria-busy", "false");
      $("load-more").disabled = false;
    }
  }
}

function inputValue() {
  return { title: $("note-title").value.trim(), content: $("note-content").value, pinned: $("note-pinned").checked };
}

function isDirty() { return $("editor").open && state.snapshot !== JSON.stringify(inputValue()); }

function updateCount() { $("char-count").textContent = `${Array.from($("note-content").value).length.toLocaleString("ru-RU")} / 100 000`; }

function openEditor(note = null) {
  if (!state.user) return;
  state.editing = note;
  $("editor-heading").textContent = note ? "Редактировать заметку" : "Новая заметка";
  $("note-title").value = note?.title || "";
  $("note-content").value = note?.content || "";
  $("note-pinned").checked = Boolean(note?.pinned);
  $("delete-note").hidden = !note;
  $("form-error").hidden = true;
  state.snapshot = JSON.stringify(inputValue());
  updateCount();
  $("editor").showModal();
  $("note-title").focus();
}

function closeEditor() {
  if (state.saving) return;
  if (isDirty() && !window.confirm("Закрыть заметку без сохранения изменений?")) return;
  $("editor").close();
  state.editing = null;
}

function setSaving(value) {
  state.saving = value;
  for (const control of $("note-form").elements) control.disabled = value;
  $("save-note").textContent = value ? "Сохранение…" : "Сохранить";
}

function showToast(message) {
  clearTimeout(state.timer);
  $("toast").textContent = message;
  $("toast").hidden = false;
  state.timer = setTimeout(() => { $("toast").hidden = true; }, 3500);
}

$("note-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  if (state.saving) return;
  const input = inputValue();
  $("form-error").hidden = true;
  if (!input.title || Array.from(input.title).length > 200 || Array.from(input.content).length > 100000) {
    $("form-error").textContent = "Укажите заголовок до 200 символов и текст до 100 000 символов.";
    $("form-error").hidden = false;
    return;
  }
  const id = state.editing?.id;
  setSaving(true);
  try {
    await api(id ? `/api/notes/${id}` : "/api/notes", { method: id ? "PUT" : "POST", body: JSON.stringify(input) });
    $("editor").close();
    state.editing = null;
    showToast(id ? "Изменения сохранены" : "Заметка создана");
    await loadNotes();
  } catch (error) {
    $("form-error").textContent = error.message;
    $("form-error").hidden = false;
  } finally { setSaving(false); }
});

$("delete-note").addEventListener("click", async () => {
  if (state.saving || !state.editing || !window.confirm("Удалить заметку? Это действие нельзя отменить.")) return;
  setSaving(true);
  $("form-error").hidden = true;
  try {
    await api(`/api/notes/${state.editing.id}`, { method: "DELETE" });
    $("editor").close();
    state.editing = null;
    showToast("Заметка удалена");
    await loadNotes();
  } catch (error) {
    $("form-error").textContent = error.message;
    $("form-error").hidden = false;
  } finally { setSaving(false); }
});

for (const id of ["new-note", "empty-create"]) $(id).addEventListener("click", () => openEditor());
for (const id of ["close-editor", "cancel-editor"]) $(id).addEventListener("click", closeEditor);
$("editor").addEventListener("cancel", (event) => { event.preventDefault(); closeEditor(); });
$("note-content").addEventListener("input", updateCount);
$("retry").addEventListener("click", () => loadNotes());
$("load-more").addEventListener("click", () => loadNotes(true));
let searchTimer;
$("search").addEventListener("input", () => {
  clearTimeout(searchTimer);
  // Invalidate the previous request immediately, before the debounce finishes.
  state.request?.abort();
  state.request = null;
  searchTimer = setTimeout(() => { state.query = $("search").value.trim(); loadNotes(); }, 250);
});
window.addEventListener("beforeunload", (event) => {
  if (isDirty() || state.draft) { event.preventDefault(); event.returnValue = ""; }
});

function setAuthMode(mode) {
  state.authMode = mode === "register" && state.registrationEnabled ? "register" : "login";
  const registering = state.authMode === "register";
  $("auth-heading").textContent = registering ? "Начните с чистого листа." : "С возвращением.";
  $("auth-description").textContent = registering ? "Создайте учётную запись для своих заметок." : "Войдите, чтобы открыть свои заметки.";
  $("login-tab").setAttribute("aria-pressed", String(!registering));
  $("register-tab").setAttribute("aria-pressed", String(registering));
  $("register-tab").hidden = !state.registrationEnabled;
  $("password-confirmation").hidden = !registering;
  $("register-hint").hidden = !registering;
  $("auth-confirm").required = registering;
  $("auth-password").minLength = registering ? 12 : 1;
  $("auth-password").autocomplete = registering ? "new-password" : "current-password";
  $("auth-submit").textContent = registering ? "Создать учётную запись" : "Войти";
  $("auth-error").hidden = true;
  $("auth-password").value = "";
  $("auth-confirm").value = "";
}

function showAuth(message = "") {
  if ($("editor").open) {
    state.draft = { userID: state.user?.id, note: state.editing, input: inputValue() };
    $("editor").close();
  }
  state.user = null;
  state.editing = null;
  state.notes = [];
  state.total = 0;
  state.query = "";
  clearTimeout(searchTimer);
  state.request?.abort();
  state.request = null;
  $("notes").replaceChildren();
  $("search").value = "";
  $("app-shell").hidden = true;
  $("auth-screen").hidden = false;
  $("toast").hidden = true;
  $("user-name").textContent = "";
  setAuthMode("login");
  $("auth-error").textContent = message;
  $("auth-error").hidden = !message;
}

async function activateUser(user) {
  state.user = user;
  $("user-name").textContent = user.username;
  $("user-name").title = user.username;
  $("auth-screen").hidden = true;
  $("app-shell").hidden = false;
  $("auth-password").value = "";
  $("auth-confirm").value = "";
  await loadNotes();
  if (state.user && state.draft?.userID === user.id) {
    const draft = state.draft;
    openEditor(draft.note);
    $("note-title").value = draft.input.title;
    $("note-content").value = draft.input.content;
    $("note-pinned").checked = draft.input.pinned;
    updateCount();
    showToast("Несохранённый текст восстановлен");
  }
  state.draft = null;
}

function authBusy(value) {
  state.authBusy = value;
  for (const control of $("auth-form").elements) control.disabled = value;
  $("login-tab").disabled = value;
  $("register-tab").disabled = value;
  $("auth-submit").textContent = value ? "Подождите…" : state.authMode === "register" ? "Создать учётную запись" : "Войти";
}

async function initAuth() {
  authBusy(true);
  $("auth-retry").hidden = true;
  try {
    const config = await api("/api/auth/config");
    state.registrationEnabled = config.registration_enabled;
    setAuthMode("login");
    try {
      const data = await api("/api/auth/me");
      await activateUser(data.user);
    } catch (error) {
      if (error.status === 401) showAuth();
      else throw error;
    }
  } catch (error) {
    showAuth(error.message);
    $("auth-retry").hidden = false;
  } finally { authBusy(false); }
}

$("login-tab").addEventListener("click", () => setAuthMode("login"));
$("register-tab").addEventListener("click", () => setAuthMode("register"));
$("auth-retry").addEventListener("click", initAuth);
$("auth-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  if (state.authBusy) return;
  $("auth-error").hidden = true;
  const password = $("auth-password").value;
  if (state.authMode === "register" && password !== $("auth-confirm").value) {
    $("auth-error").textContent = "Пароли не совпадают.";
    $("auth-error").hidden = false;
    return;
  }
  authBusy(true);
  try {
    const data = await api(`/api/auth/${state.authMode}`, { method: "POST", body: JSON.stringify({ username: $("auth-username").value.trim(), password }) });
    await activateUser(data.user);
  } catch (error) {
    $("auth-error").textContent = error.message;
    $("auth-error").hidden = false;
  } finally { authBusy(false); }
});

$("logout").addEventListener("click", async () => {
  if (state.saving) return;
  $("logout").disabled = true;
  try {
    await api("/api/auth/logout", { method: "POST" });
    state.draft = null;
    showAuth();
  } catch (error) {
    if (error.status === 401) showAuth();
    else showToast(error.message);
  } finally { $("logout").disabled = false; }
});
initAuth();
