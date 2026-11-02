// APOD Explorer frontend. Plain ES modules, no build step.
//
// Rendering rule: API data is only ever inserted as text (see dom.js), so
// titles or explanations containing HTML can't inject markup or script.

import { $, h, show, formatLong, formatShort, timeEl, apodToday, FIRST_DAY, daysInclusive, storage } from "./dom.js";
import { api, ApiError } from "./api.js";
import { buildMedia, fadeImage, thumbnailOf, safeUrl } from "./media.js";
import { Favorites } from "./favorites.js";
import { startStarfield } from "./starfield.js";

const PAGE_SIZE = 9;
const TODAY = apodToday();

const state = {
  user: null,
  view: "explore", // explore | favorites | history
  galleryMode: "random", // random | results | favorites
  galleryItems: [],
  loadingMore: false,
  hero: null,
  viewer: { list: [], index: 0, hd: false, token: 0 },
  authMode: "login",
  noticeShown: "",
};

const favorites = new Favorites(api);

// ---------------------------------------------------------------------------
// Small UI utilities
// ---------------------------------------------------------------------------

let toastTimer;
function toast(message) {
  const el = $("#toast");
  el.textContent = message;
  if (el.showPopover) {
    // Re-showing moves it to the top of the top layer, above any dialog
    // opened since it was last shown.
    if (el.matches(":popover-open")) el.hidePopover();
    el.showPopover();
  }
  el.classList.add("is-visible");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => {
    el.classList.remove("is-visible");
    if (el.hidePopover && el.matches(":popover-open")) el.hidePopover();
  }, 3500);
}

/** Tells screen-reader users about background updates without a visible toast. */
function announce(message) {
  const el = $("#announcer");
  el.textContent = "";
  requestAnimationFrame(() => (el.textContent = message));
}

/** Shows an error with a retry button inside container. */
function showError(container, err, retry) {
  container.replaceChildren(
    h("p", {}, err instanceof ApiError ? err.message : "Something went wrong while loading this."),
  );
  if (retry) {
    const button = h("button", { type: "button", class: "button" }, "Try again");
    button.addEventListener("click", () => {
      show(container, false);
      retry();
    });
    container.append(button);
  }
  show(container, true);
}

/** Banner for demo mode or NASA placeholder data (demo mode wins). */
function noteDataFlags(items) {
  const list = Array.isArray(items) ? items : [items];
  let kind = "";
  if (list.some((it) => it?.sample)) kind = "sample";
  else if (list.some((it) => it?.placeholder)) kind = "placeholder";
  if (!kind || state.noticeShown === "sample" || state.noticeShown === kind) return;

  state.noticeShown = kind;
  const notice = $("#notice");
  notice.textContent =
    kind === "sample"
      ? "Demo mode: you're seeing sample NASA images, not the real pictures of the day."
      : "NASA's picture service is sending placeholder entries right now, so some days show the NASA logo instead of their picture.";
  show(notice, true);
}

/** The visual for a card: thumbnail, or a labelled tile when there is none. */
function cardVisual(item, { eager = false } = {}) {
  const isVideo = item.media_type === "video";
  const thumb = thumbnailOf(item);
  const wrap = h("div", { class: `card-visual${isVideo ? " is-video" : ""}` });
  wrap.append(
    thumb
      ? fadeImage(thumb, isVideo ? `Video: ${item.title}` : item.title, { loading: eager ? "eager" : "lazy" })
      : h("div", { class: "no-thumb" }, isVideo ? "Video" : "No preview"),
  );
  if (isVideo) wrap.append(h("span", { class: "play-badge", "aria-hidden": "true" }));
  return wrap;
}

const heart = () =>
  h("span", { class: "heart", "aria-hidden": "true" });

// ---------------------------------------------------------------------------
// Favorites UI
// ---------------------------------------------------------------------------

function updateFavCount() {
  $("#fav-count").textContent = favorites.count ? String(favorites.count) : "";
}

function setFavButton(button, date, title) {
  const saved = favorites.has(date);
  button.setAttribute("aria-pressed", String(saved));
  if (button.classList.contains("fav-toggle")) {
    button.setAttribute("aria-label", saved ? `Remove ${title} from favorites` : `Save ${title} to favorites`);
  } else {
    button.textContent = saved ? "Saved to favorites" : "Save to favorites";
  }
}

/** Refresh every favorite button showing this date. */
function refreshFavButtons(date) {
  document.querySelectorAll(`[data-fav-date="${CSS.escape(date)}"]`).forEach((b) => setFavButton(b, date, b.dataset.favTitle));
  updateFavCount();
}

let localSaveHintShown = false;
async function toggleFavorite(item) {
  try {
    const saved = await favorites.toggle(item);
    refreshFavButtons(item.date);
    if (saved && !state.user && !localSaveHintShown) {
      localSaveHintShown = true;
      toast("Saved in this browser. Log in to keep favorites on every device.");
    } else {
      toast(saved ? "Saved to favorites" : "Removed from favorites");
    }
    if (state.view === "favorites") renderFavoritesGallery();
  } catch (err) {
    toast(err.message);
  }
}

function bindFavButton(button, item) {
  button.dataset.favDate = item.date;
  button.dataset.favTitle = item.title;
  setFavButton(button, item.date, item.title);
}

// ---------------------------------------------------------------------------
// Hero: today's picture
// ---------------------------------------------------------------------------

function excerpt(text, max = 280) {
  if (!text) return "";
  if (text.length <= max) return text;
  const cut = text.slice(0, max);
  const sentenceEnd = cut.lastIndexOf(". ");
  return (sentenceEnd > 120 ? cut.slice(0, sentenceEnd + 1) : `${cut.slice(0, cut.lastIndexOf(" "))}…`).trim();
}

async function loadHero() {
  const section = $(".hero");
  section.setAttribute("aria-busy", "true");
  show($("#hero-error"), false);
  try {
    const item = await api.today();
    state.hero = item;
    noteDataFlags(item);
    renderHero(item);
    loadStrip(item.date);
  } catch (err) {
    $("#hero-media").replaceChildren(h("div", { class: "no-thumb hero-empty" }, "Today's picture isn't available."));
    $("#hero-title").textContent = "Today's picture didn't load";
    showError($("#hero-error"), err, loadHero);
    loadStrip(TODAY);
  } finally {
    section.setAttribute("aria-busy", "false");
  }
}

function renderHero(item) {
  $("#hero-kicker").textContent = item.date === TODAY ? "Today's picture" : "Latest picture";
  $("#hero-title").textContent = item.title;
  $("#hero-date").replaceChildren(timeEl(item.date));
  $("#hero-excerpt").textContent = excerpt(item.explanation);

  const open = h("button", { type: "button", class: "hero-media-button", "aria-label": `View full picture: ${item.title}` },
    cardVisual(item, { eager: true }));
  open.addEventListener("click", () => openViewer([item], 0));
  $("#hero-media").replaceChildren(open);

  bindFavButton($("#hero-fav"), item);
  show($("#hero-actions"), true);
}

// ---------------------------------------------------------------------------
// "On this day" strip
// ---------------------------------------------------------------------------

async function loadStrip(date) {
  const list = $("#strip-list");
  list.replaceChildren(...Array.from({ length: 5 }, () => h("li", { class: "strip-item", "aria-hidden": "true" }, h("div", { class: "skeleton skeleton-strip" }))));
  $("#strip-note").textContent = `${formatLong(date).replace(/\s?\d{4}$/, "")} in earlier years.`;

  try {
    const items = await api.onThisDay(date, 6);
    noteDataFlags(items);
    if (!items.length) {
      list.replaceChildren(h("li", { class: "muted" }, "There are no earlier years to show for this date yet."));
      return;
    }
    list.replaceChildren(
      ...items.map((item, i) => {
        const button = h("button", { type: "button", class: "strip-card" },
          h("span", { class: "strip-year" }, item.date.slice(0, 4)),
          cardVisual(item),
          h("span", { class: "strip-title" }, item.title));
        button.addEventListener("click", () => openViewer(items, i));
        return h("li", { class: "strip-item" }, button);
      }),
    );
  } catch (err) {
    const box = h("li", { class: "inline-error" });
    list.replaceChildren(box);
    showError(box, err, () => loadStrip(date));
  }
}

// ---------------------------------------------------------------------------
// Gallery
// ---------------------------------------------------------------------------

function setGallery(mode, title, note) {
  state.galleryMode = mode;
  state.galleryItems = [];
  $("#gallery").replaceChildren();
  $("#gallery-title").textContent = title;
  $("#gallery-note").textContent = note;
  show($("#back-to-explore"), mode === "results");
  show($("#load-more"), mode === "random");
  show($("#gallery-empty"), false);
  show($("#gallery-error"), false);
}

function makeCard(item) {
  const index = state.galleryItems.length;
  state.galleryItems.push(item);

  const open = h("button", { type: "button", class: "card-open" },
    cardVisual(item),
    h("span", { class: "card-body" },
      h("span", { class: "card-title" }, item.title),
      h("span", { class: "card-date" }, timeEl(item.date, formatShort))));
  open.addEventListener("click", () => openViewer(state.galleryItems, index));

  const fav = h("button", { type: "button", class: "fav-toggle" }, heart());
  bindFavButton(fav, item);
  fav.addEventListener("click", () => toggleFavorite(item));

  return h("li", { class: "card" }, open, fav);
}

function addSkeletons(n) {
  const nodes = Array.from({ length: n }, (_, i) =>
    h("li", { class: "card is-skeleton", "aria-hidden": "true" },
      h("div", { class: `skeleton skeleton-card h${i % 3}` }),
      h("div", { class: "skeleton skeleton-line" })));
  $("#gallery").append(...nodes);
  return () => nodes.forEach((n) => n.remove());
}

async function loadMoreRandom() {
  if (state.loadingMore || state.galleryMode !== "random" || state.view !== "explore") return;
  state.loadingMore = true;
  show($("#gallery-error"), false);
  $("#load-more").disabled = true;
  const removeSkeletons = addSkeletons(PAGE_SIZE);
  try {
    const seen = new Set(state.galleryItems.map((i) => i.date));
    const items = (await api.random(PAGE_SIZE)).filter((i) => !seen.has(i.date));
    noteDataFlags(items);
    removeSkeletons();
    $("#gallery").append(...items.map(makeCard));
    announce(`Loaded ${items.length} more pictures`);
  } catch (err) {
    removeSkeletons();
    showError($("#gallery-error"), err, loadMoreRandom);
  } finally {
    state.loadingMore = false;
    $("#load-more").disabled = false;
  }
}

async function showRange(start, end) {
  switchView("explore", { keepGallery: true });
  setGallery("results", `${formatLong(start)} to ${formatLong(end)}`, `${daysInclusive(start, end)} days of pictures.`);
  $("#gallery-section").scrollIntoView({ behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth" });
  const removeSkeletons = addSkeletons(Math.min(daysInclusive(start, end), PAGE_SIZE));
  try {
    const items = await api.range(start, end);
    noteDataFlags(items);
    removeSkeletons();
    if (!items.length) {
      $("#gallery-empty").textContent = "NASA has no pictures for those dates.";
      show($("#gallery-empty"), true);
      return;
    }
    $("#gallery").append(...items.map(makeCard));
  } catch (err) {
    removeSkeletons();
    showError($("#gallery-error"), err, () => showRange(start, end));
  }
}

function renderFavoritesGallery() {
  const items = favorites.list().map((f) => ({ ...f, media_type: "image", partial: true }));
  setGallery("favorites", "Your favorites", state.user
    ? "Saved to your account."
    : "Saved in this browser. Log in to keep them on every device.");
  if (!items.length) {
    $("#gallery-empty").textContent = "No favorites yet. Open any picture and choose Save to favorites.";
    show($("#gallery-empty"), true);
    return;
  }
  $("#gallery").append(...items.map(makeCard));
}

// ---------------------------------------------------------------------------
// History
// ---------------------------------------------------------------------------

const viewedFormat = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });

async function loadHistory() {
  const list = $("#history-list");
  list.replaceChildren();
  show($("#history-empty"), false);
  show($("#history-error"), false);
  try {
    const entries = await api.history();
    if (!entries.length) {
      $("#history-empty").textContent = "Pictures you open while logged in will appear here.";
      show($("#history-empty"), true);
      return;
    }
    const items = entries.map((e) => ({ date: e.date, title: e.title, partial: true }));
    list.append(
      ...entries.map((e, i) => {
        const button = h("button", { type: "button", class: "history-item" },
          h("span", { class: "history-title" }, e.title || "Untitled"),
          h("span", { class: "history-meta" }, timeEl(e.date), ", viewed ", viewedFormat.format(new Date(e.viewed_at))));
        button.addEventListener("click", () => openViewer(items, i));
        return h("li", {}, button);
      }),
    );
  } catch (err) {
    showError($("#history-error"), err, loadHistory);
  }
}

// ---------------------------------------------------------------------------
// Views
// ---------------------------------------------------------------------------

function switchView(view, { keepGallery = false } = {}) {
  if (view === "history" && !state.user) return openAuth("login", "Log in to see the pictures you've viewed.");
  state.view = view;
  document.querySelectorAll(".nav-link").forEach((b) => {
    if (b.dataset.view === view) b.setAttribute("aria-current", "page");
    else b.removeAttribute("aria-current");
  });
  show($("#view-explore"), view === "explore");
  show($("#gallery-section"), view !== "history");
  show($("#view-history"), view === "history");

  if (view === "favorites") renderFavoritesGallery();
  if (view === "history") loadHistory();
  if (view === "explore" && !keepGallery && state.galleryMode !== "random") {
    setGallery("random", "More from the archive", "Random days from three decades of pictures.");
    loadMoreRandom();
  }
}

// ---------------------------------------------------------------------------
// Picture viewer (lightbox)
// ---------------------------------------------------------------------------

const viewer = $("#viewer");

function openViewer(list, index) {
  state.viewer = { list: [...list], index, hd: false, token: state.viewer.token };
  if (!viewer.open) viewer.showModal();
  renderViewer();
}

function closeViewer() {
  if (viewer.open) viewer.close();
}

function step(delta) {
  const next = state.viewer.index + delta;
  if (next < 0 || next >= state.viewer.list.length) return;
  state.viewer.index = next;
  state.viewer.hd = false;
  renderViewer();
}

async function renderViewer() {
  const token = ++state.viewer.token; // guards against out-of-order async results
  const { list, index } = state.viewer;
  let item = list[index];

  $("#viewer-prev").disabled = index === 0;
  $("#viewer-next").disabled = index === list.length - 1;
  $("#tool-output").replaceChildren();
  setDeepLink(item.date);

  if (item.partial || item.explanation === undefined) {
    $("#viewer-media").replaceChildren(h("div", { class: "skeleton skeleton-viewer" }));
    $("#viewer-title").textContent = item.title || "Loading…";
    $("#viewer-date").replaceChildren(timeEl(item.date));
    $("#viewer-explanation").replaceChildren();
    try {
      item = await api.day(item.date);
    } catch (err) {
      if (token !== state.viewer.token) return;
      const box = h("div", { class: "inline-error" });
      $("#viewer-media").replaceChildren(box);
      showError(box, err, renderViewer);
      return;
    }
    if (token !== state.viewer.token) return;
    list[index] = item;
  }
  noteDataFlags(item);

  $("#viewer-media").replaceChildren(buildMedia(item, { hd: state.viewer.hd }));
  $("#viewer-date").replaceChildren(timeEl(item.date));
  $("#viewer-title").textContent = item.title;
  $("#viewer-credit").textContent = item.copyright ? `Credit: ${item.copyright.trim()}` : "Public domain";

  const flag = $("#viewer-flag");
  flag.textContent = item.sample
    ? "Sample image shown in demo mode."
    : item.placeholder
      ? "NASA sent a placeholder for this day instead of its picture."
      : "";
  show(flag, Boolean(item.sample || item.placeholder));

  const paragraphs = (item.explanation || "No explanation was provided for this entry.").split(/\n\s*\n/);
  $("#viewer-explanation").replaceChildren(...paragraphs.map((p) => h("p", {}, p.trim())));

  const hdButton = $("#viewer-hd");
  const hasHD = item.media_type !== "video" && item.hdurl && item.hdurl !== item.url;
  show(hdButton, Boolean(hasHD));
  hdButton.setAttribute("aria-pressed", String(state.viewer.hd));
  hdButton.textContent = state.viewer.hd ? "Show standard size" : "Show HD";

  const original = $("#viewer-original");
  original.href = safeUrl(item.hdurl || item.url) || "#";
  original.textContent = item.media_type === "video" ? "Open video" : "Open full size";

  bindFavButton($("#viewer-fav"), item);
  updateCommentControls();
  if (state.user) api.recordView(item).catch(() => {}); // history is best-effort
  loadRating(item.date, token);
  loadComments(item.date, token);
  preloadNeighbours();
}

function currentItem() {
  return state.viewer.list[state.viewer.index];
}

function preloadNeighbours() {
  for (const d of [-1, 1]) {
    const it = state.viewer.list[state.viewer.index + d];
    const src = it && !it.partial ? thumbnailOf(it) : "";
    if (src) new Image().src = src;
  }
}

/** Keep ?date=YYYY-MM-DD in the address bar so the open picture can be shared. */
function setDeepLink(date) {
  const url = new URL(location.href);
  if (date) url.searchParams.set("date", date);
  else url.searchParams.delete("date");
  history.replaceState(null, "", url);
}

// ---- rating ----

function buildStars() {
  const wrap = $("#stars");
  for (let n = 5; n >= 1; n--) {
    // Reversed DOM order lets CSS highlight "this star and all before it".
    const id = `star-${n}`;
    wrap.append(
      h("input", { type: "radio", name: "rating", id, value: n, class: "visually-hidden" }),
      h("label", { for: id, title: `${n} out of 5` }, h("span", { class: "visually-hidden" }, `${n} out of 5`)),
    );
  }
  wrap.addEventListener("change", async (e) => {
    const value = Number(e.target.value);
    const item = currentItem();
    if (!state.user) {
      e.target.checked = false;
      return openAuth("login", "Log in to rate pictures.");
    }
    try {
      await api.rate(item.date, value);
      toast(`Rated ${value} out of 5`);
      loadRating(item.date, state.viewer.token);
    } catch (err) {
      toast(err.message);
    }
  });
}

async function loadRating(date, token) {
  document.querySelectorAll("#stars input").forEach((i) => (i.checked = false));
  const out = $("#rating-average");
  out.textContent = "";
  try {
    const { average } = await api.rating(date);
    if (token !== state.viewer.token) return;
    out.textContent = average > 0 ? `Average rating ${average} out of 5` : "No ratings yet. Be the first.";
  } catch {
    if (token === state.viewer.token) out.textContent = "Ratings are unavailable right now.";
  }
}

// ---- comments ----

const commentTime = new Intl.DateTimeFormat(undefined, { dateStyle: "medium" });

async function loadComments(date, token) {
  const list = $("#comments");
  list.replaceChildren();
  try {
    const comments = await api.comments(date);
    if (token !== state.viewer.token) return;
    if (!comments.length) {
      list.append(h("li", { class: "muted" }, "No comments yet."));
      return;
    }
    list.append(
      ...comments.map((c) =>
        h("li", { class: "comment" },
          h("p", { class: "comment-meta" }, h("strong", {}, c.username), " on ",
            h("time", { datetime: c.created_at }, commentTime.format(new Date(c.created_at)))),
          h("p", { class: "comment-text" }, c.comment))),
    );
  } catch {
    if (token === state.viewer.token) list.append(h("li", { class: "muted" }, "Comments are unavailable right now."));
  }
}

function updateCommentControls() {
  show($("#comment-form"), Boolean(state.user));
  show($("#comment-login"), !state.user);
}

// ---- learning tools ----

async function runTool(task, extra = {}) {
  const item = currentItem();
  if (!item || item.partial) return;
  const out = $("#tool-output");
  out.setAttribute("aria-busy", "true");
  out.replaceChildren(h("p", { class: "muted" }, "Working on it…"));
  try {
    const res = await api.explain(task, item, extra);
    out.replaceChildren(
      h("div", { class: "tool-answer" }, res.response),
      h("p", { class: "tool-source" },
        res.source === "llm" ? "Generated answer. Check important facts against the explanation above." : "Built-in answer: live answers aren't switched on for this server."),
    );
  } catch (err) {
    out.replaceChildren(h("p", { class: "form-error" }, err.message));
  } finally {
    out.setAttribute("aria-busy", "false");
  }
}

function setupVoice() {
  const Recognition = window.SpeechRecognition || window.webkitSpeechRecognition;
  if (!Recognition) return; // button stays hidden where unsupported
  const button = $("#voice-button");
  show(button, true);
  button.addEventListener("click", () => {
    const rec = new Recognition();
    rec.lang = document.documentElement.lang || "en-US";
    rec.addEventListener("result", (e) => {
      const question = e.results[0][0].transcript;
      $("#ask-input").value = question;
      runTool("chat", { question });
    });
    rec.addEventListener("error", () => toast("Couldn't hear a question. Try typing it instead."));
    rec.start();
    toast("Listening…");
  });
}

// ---------------------------------------------------------------------------
// Accounts
// ---------------------------------------------------------------------------

const authDialog = $("#auth");

function setAuthMode(mode, lede) {
  state.authMode = mode;
  const signup = mode === "signup";
  $("#auth-title").textContent = signup ? "Create an account" : "Log in";
  $("#auth-lede").textContent = lede || (signup ? "Your favorites will follow you to every device." : "Log in to keep your favorites on every device.");
  $("#auth-username-label").textContent = signup ? "Username" : "Username or email";
  show($("#email-field"), signup);
  $("#auth-email").required = signup;
  $("#auth-password").autocomplete = signup ? "new-password" : "current-password";
  show($("#password-hint"), signup);
  $("#auth-submit").textContent = signup ? "Create account" : "Log in";
  $("#auth-switch-text").textContent = signup ? "Already have an account?" : "New here?";
  $("#auth-switch").textContent = signup ? "Log in" : "Create an account";
  show($("#auth-error"), false);
}

function openAuth(mode = "login", lede = "") {
  setAuthMode(mode, lede);
  $("#auth-form").reset();
  authDialog.showModal();
  $("#auth-username").focus();
}

function authError(message) {
  const el = $("#auth-error");
  el.textContent = message;
  show(el, true);
}

async function submitAuth(e) {
  e.preventDefault();
  const username = $("#auth-username").value.trim();
  const email = $("#auth-email").value.trim();
  const password = $("#auth-password").value;
  const signup = state.authMode === "signup";

  if (!username || !password) return authError("Enter your username and password.");
  if (signup && !email) return authError("Enter your email address.");
  if (signup && password.length < 8) return authError("Use a password with at least 8 characters.");

  const submit = $("#auth-submit");
  submit.disabled = true;
  try {
    state.user = signup ? await api.signup(username, email, password) : await api.login(username, password);
    authDialog.close();
    const moved = await favorites.syncAfterLogin();
    updateAuthUI();
    updateFavCount();
    toast(
      `${signup ? "Account created. " : ""}Logged in as ${state.user.username}.` +
        (moved ? ` Moved ${moved} ${moved === 1 ? "favorite" : "favorites"} to your account.` : ""),
    );
    if (state.view === "favorites") renderFavoritesGallery();
  } catch (err) {
    authError(err.message);
  } finally {
    submit.disabled = false;
  }
}

async function logout() {
  try {
    await api.logout();
  } catch {
    /* the cookie is cleared locally either way */
  }
  state.user = null;
  await favorites.load(false);
  updateAuthUI();
  updateFavCount();
  document.querySelectorAll("[data-fav-date]").forEach((b) => setFavButton(b, b.dataset.favDate, b.dataset.favTitle));
  toast("Logged out");
  if (state.view === "history") switchView("explore");
  else if (state.view === "favorites") renderFavoritesGallery();
}

function updateAuthUI() {
  const name = $("#user-name");
  name.textContent = state.user ? `Signed in as ${state.user.username}` : "";
  show(name, Boolean(state.user));
  show($("#login-button"), !state.user);
  show($("#logout-button"), Boolean(state.user));
  show($("#nav-history"), Boolean(state.user));
  updateCommentControls();
}

async function loadUser() {
  try {
    state.user = await api.me();
  } catch {
    state.user = null; // 401 simply means "not logged in"
  }
  try {
    await favorites.load(Boolean(state.user));
  } catch {
    await favorites.load(false);
  }
  updateAuthUI();
  updateFavCount();
}

// ---------------------------------------------------------------------------
// Search forms
// ---------------------------------------------------------------------------

function searchError(message) {
  const el = $("#search-error");
  el.textContent = message;
  show(el, Boolean(message));
}

function inArchive(date) {
  return date >= FIRST_DAY && date <= TODAY;
}

function setupSearch() {
  for (const id of ["#date-input", "#start-input", "#end-input"]) {
    $(id).min = FIRST_DAY;
    $(id).max = TODAY;
  }
  const year = $("#year-input");
  year.max = TODAY.slice(0, 4);
  year.value = String(Number(TODAY.slice(0, 4)) - 10);
  $("#year-output").textContent = year.value;
  year.addEventListener("input", () => ($("#year-output").textContent = year.value));

  $("#date-form").addEventListener("submit", (e) => {
    e.preventDefault();
    const date = $("#date-input").value;
    if (!inArchive(date)) return searchError(`Pick a date from ${formatLong(FIRST_DAY)} to today.`);
    searchError("");
    openViewer([{ date, title: "", partial: true }], 0);
  });

  $("#range-form").addEventListener("submit", (e) => {
    e.preventDefault();
    const start = $("#start-input").value;
    const end = $("#end-input").value;
    if (!inArchive(start) || !inArchive(end)) return searchError(`Pick dates from ${formatLong(FIRST_DAY)} to today.`);
    if (end < start) return searchError("The end date needs to be on or after the start date.");
    if (daysInclusive(start, end) > 30) return searchError("Choose a range of 30 days or fewer.");
    searchError("");
    showRange(start, end);
  });

  $("#year-form").addEventListener("submit", (e) => {
    e.preventDefault();
    let date = `${year.value}${TODAY.slice(4)}`;
    if (date.endsWith("-02-29") && new Date(`${date}T00:00:00Z`).getUTCDate() !== 29) date = date.replace("-29", "-28");
    if (date < FIRST_DAY) date = FIRST_DAY;
    if (date > TODAY) date = TODAY;
    searchError("");
    openViewer([{ date, title: "", partial: true }], 0);
  });
}

// ---------------------------------------------------------------------------
// Space facts
// ---------------------------------------------------------------------------

async function loadFacts() {
  const list = $("#facts-list");
  try {
    const f = await api.facts();
    const fmt = new Intl.NumberFormat();
    const rows = [
      [fmt.format(f.planets), "planets"],
      [fmt.format(f.moons), "known moons"],
      [`${fmt.format(f.avg_gravity)} m/s²`, "average surface gravity"],
      [`${fmt.format(f.avg_radius)} km`, "average radius"],
    ];
    list.replaceChildren(...rows.flatMap(([value, label]) => [h("div", {}, h("dt", {}, label), h("dd", {}, value))]));
  } catch {
    list.replaceChildren(h("p", { class: "muted" }, "Solar system figures are unavailable right now."));
  }
}

// ---------------------------------------------------------------------------
// Theme
// ---------------------------------------------------------------------------

function applyTheme(theme) {
  document.documentElement.dataset.theme = theme;
  const button = $("#theme-toggle");
  const light = theme === "light";
  button.setAttribute("aria-pressed", String(light));
  button.setAttribute("aria-label", light ? "Use dark theme" : "Use light theme");
}

// ---------------------------------------------------------------------------
// Wiring
// ---------------------------------------------------------------------------

function bindEvents() {
  document.querySelectorAll(".nav-link").forEach((b) => b.addEventListener("click", () => switchView(b.dataset.view)));
  $("#back-to-explore").addEventListener("click", () => switchView("explore"));
  $("#load-more").addEventListener("click", loadMoreRandom);

  $("#theme-toggle").addEventListener("click", () => {
    const next = document.documentElement.dataset.theme === "light" ? "dark" : "light";
    storage.set("apod.theme", next);
    applyTheme(next);
  });

  $("#login-button").addEventListener("click", () => openAuth("login"));
  $("#logout-button").addEventListener("click", logout);
  $("#auth-close").addEventListener("click", () => authDialog.close());
  $("#auth-switch").addEventListener("click", () => setAuthMode(state.authMode === "login" ? "signup" : "login"));
  $("#auth-form").addEventListener("submit", submitAuth);

  $("#hero-open").addEventListener("click", () => state.hero && openViewer([state.hero], 0));
  $("#hero-fav").addEventListener("click", () => state.hero && toggleFavorite(state.hero));

  // Viewer controls.
  $("#viewer-close").addEventListener("click", closeViewer);
  $("#viewer-prev").addEventListener("click", () => step(-1));
  $("#viewer-next").addEventListener("click", () => step(1));
  $("#viewer-fav").addEventListener("click", () => toggleFavorite(currentItem()));
  $("#viewer-hd").addEventListener("click", () => {
    state.viewer.hd = !state.viewer.hd;
    renderViewer();
  });
  $("#viewer-share").addEventListener("click", async () => {
    const url = new URL(location.href);
    url.searchParams.set("date", currentItem().date);
    try {
      await navigator.clipboard.writeText(url.href);
      toast("Link copied");
    } catch {
      toast(`Copy this link: ${url.href}`);
    }
  });
  viewer.addEventListener("keydown", (e) => {
    if (e.target.closest("input, textarea, select, [contenteditable]")) return;
    if (e.key === "ArrowLeft") step(-1);
    if (e.key === "ArrowRight") step(1);
  });
  // A click on the backdrop (the dialog itself, outside the content) closes it.
  viewer.addEventListener("click", (e) => {
    if (e.target === viewer) closeViewer();
  });
  viewer.addEventListener("close", () => {
    $("#viewer-media").replaceChildren(); // stops any playing video
    setDeepLink("");
  });
  authDialog.addEventListener("click", (e) => {
    if (e.target === authDialog) authDialog.close();
  });

  $("#comment-login").addEventListener("click", () => openAuth("login", "Log in to join the conversation."));
  $("#comment-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    const input = $("#comment-input");
    const text = input.value.trim();
    if (!text) return;
    try {
      await api.addComment(currentItem().date, text);
      input.value = "";
      toast("Comment posted");
      loadComments(currentItem().date, state.viewer.token);
    } catch (err) {
      toast(err.message);
    }
  });

  document.querySelectorAll("#tools .chip").forEach((chip) =>
    chip.addEventListener("click", () => runTool(chip.dataset.task, chip.dataset.level ? { level: chip.dataset.level } : {})));
  $("#ask-form").addEventListener("submit", (e) => {
    e.preventDefault();
    const question = $("#ask-input").value.trim();
    if (question) runTool("chat", { question });
  });
  $("#distance-form").addEventListener("submit", (e) => {
    e.preventDefault();
    runTool("distance", { distance: $("#distance-input").value.trim() });
  });

  // Infinite scroll: load the next batch shortly before the end is visible.
  new IntersectionObserver((entries) => entries[0].isIntersecting && loadMoreRandom(), { rootMargin: "600px" })
    .observe($("#sentinel"));
}

async function init() {
  applyTheme(storage.get("apod.theme", "dark"));
  startStarfield($("#starfield"));
  buildStars();
  setupSearch();
  setupVoice();
  bindEvents();

  await loadUser();
  loadHero();
  loadFacts();
  loadMoreRandom();

  // Deep link: /?date=YYYY-MM-DD opens that picture straight away.
  const linked = new URLSearchParams(location.search).get("date");
  if (linked) {
    if (/^\d{4}-\d{2}-\d{2}$/.test(linked) && inArchive(linked)) openViewer([{ date: linked, title: "", partial: true }], 0);
    else toast("That link points to a date outside the APOD archive.");
  }
}

init();
