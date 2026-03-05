const gallery = document.getElementById("gallery");
const loader = document.getElementById("loader");
let currentItems = [];
let isLoading = false;
let mode = "infinite";
let currentModalItem = null;

function authHeaders() {
  const token = localStorage.getItem("token") || "";
  return token ? { Authorization: `Bearer ${token}` } : {};
}
let activeItem = null;

function setLoading(show) {
  loader.style.display = show ? "block" : "none";
}

async function fetchApod(params = "") {
  setLoading(true);
  try {
    const res = await fetch(`/api/apod${params}`, { headers: authHeaders() });
    const data = await res.json();
    return Array.isArray(data) ? data : [data];
  } finally {
    setLoading(false);
  }
}

function renderCards(items, append = false) {
  if (!append) gallery.innerHTML = "";
  items
    .filter((item) => (item.media_type || "image") === "image")
    .forEach((item) => {
      const card = document.createElement("article");
      card.className = "gallery-card";
      card.innerHTML = `
        <img src="${item.url}" alt="${item.title}" />
        <div class="content">
          <h3>${item.title}</h3>
          <p>${item.date}</p>
        </div>
      `;
      card.onclick = () => openModal(item);
      gallery.appendChild(card);
    });
}

async function openModal(data) {
  currentModalItem = data;
function clearAIOutputs() {
  ["rewriteOutput", "chatOutput", "featureOutput"].forEach((id) => {
    document.getElementById(id).innerText = "";
  });
}

function openModal(data) {
  activeItem = data;
  clearAIOutputs();
  document.getElementById("modal").classList.remove("hidden");
  document.getElementById("modalTitle").innerText = data.title;
  document.getElementById("modalDate").innerText = data.date;
  document.getElementById("modalDesc").innerText = data.explanation || "";
  document.getElementById("modalImg").src = data.url;
  document.getElementById("downloadBtn").href = data.hdurl || data.url;
  await loadComments();
}

function closeModal() {
  document.getElementById("modal").classList.add("hidden");
}

async function searchByDate() {
  mode = "single";
  const date = document.getElementById("dateInput").value;
  if (!date) return;
  currentItems = await fetchApod(`?date=${date}`);
  renderCards(currentItems);
}

async function searchRange() {
  mode = "range";
  const start = document.getElementById("startDate").value;
  const end = document.getElementById("endDate").value;
  if (!start || !end) return;
  currentItems = await fetchApod(`?start_date=${start}&end_date=${end}`);
  renderCards(currentItems);
}

async function saveFavorite() {
  if (!currentModalItem) return;
  const res = await fetch("/api/favorites", {
    method: "POST",
    headers: { "Content-Type": "application/json", ...authHeaders() },
    body: JSON.stringify({
      title: currentModalItem.title,
      date: currentModalItem.date,
      url: currentModalItem.url,
    }),
  });
  alert(res.ok ? "Saved to favorites!" : "Please log in first.");
}

async function showFavorites() {
  mode = "favorites";
  const res = await fetch("/api/favorites", { headers: authHeaders() });
  if (!res.ok) return alert("Login required");
  currentItems = await res.json();
  renderCards(currentItems);
}

async function showHistory() {
  const res = await fetch("/api/history", { headers: authHeaders() });
  if (!res.ok) return alert("Login required");
  const history = await res.json();
  gallery.innerHTML = history
    .map((h) => `<article class="gallery-card"><div class="content"><h3>${h.title}</h3><p>${h.date}</p><small>${h.viewed_at}</small></div></article>`)
    .join("");
}

async function loadComments() {
  if (!currentModalItem) return;
  const res = await fetch(`/api/comments?date=${currentModalItem.date}`);
  const comments = await res.json();
  document.getElementById("comments").innerHTML = comments
    .map((c) => `<div class="comment-item"><strong>${c.username}</strong>: ${c.comment}</div>`)
    .join("");
}

async function postComment() {
  if (!currentModalItem) return;
  const comment = document.getElementById("commentInput").value.trim();
  if (!comment) return;
  const res = await fetch(`/api/comments?date=${currentModalItem.date}`, {
    method: "POST",
    headers: { "Content-Type": "application/json", ...authHeaders() },
    body: JSON.stringify({ comment }),
  });
  if (!res.ok) return alert("Login required");
  document.getElementById("commentInput").value = "";
  loadComments();
}

async function submitRating() {
  if (!currentModalItem) return;
  const rating = Number(document.getElementById("ratingSelect").value);
  const res = await fetch(`/api/ratings?date=${currentModalItem.date}`, {
    method: "POST",
    headers: { "Content-Type": "application/json", ...authHeaders() },
    body: JSON.stringify({ rating }),
  });
  alert(res.ok ? "Rating saved" : "Login required");
}

function toggleTheme() {
  document.body.classList.toggle("light");
}

async function loadMoreGallery() {
  if (isLoading || mode !== "infinite") return;
  isLoading = true;
  const items = await fetchApod("?count=6");
  currentItems = currentItems.concat(items);
  renderCards(items, true);
  isLoading = false;
}

window.addEventListener("scroll", () => {
  if (window.innerHeight + window.scrollY >= document.body.offsetHeight - 200) {
    loadMoreGallery();
  }
});

async function loadFacts() {
  try {
    const res = await fetch("/api/space-facts");
    const data = await res.json();
    document.getElementById("facts").innerHTML = `
      <p>Planets: <strong>${data.planets}</strong></p>
      <p>Known moons in dataset: <strong>${data.moons}</strong></p>
      <p>Average gravity: <strong>${data.avg_gravity}</strong> m/s²</p>
      <p>Average radius: <strong>${data.avg_radius}</strong> km</p>
    `;
  } catch {
    document.getElementById("facts").innerText = "Could not load facts.";
  }
}

async function signup() {
  const username = document.getElementById("authUser").value.trim();
  const password = document.getElementById("authPass").value;
  const email = username.includes("@") ? username : `${username}@space.local`;
  const res = await fetch("/api/signup", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username, email, password }),
  });
  alert(res.ok ? "Signup successful. Please login." : "Signup failed");
}

async function login() {
  const username = document.getElementById("authUser").value.trim();
  const password = document.getElementById("authPass").value;
  const res = await fetch("/api/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username, password }),
  });
  if (!res.ok) return alert("Invalid login");
  const data = await res.json();
  localStorage.setItem("token", data.token);
  document.getElementById("authStatus").innerText = `Logged in as ${username}`;
}

async function logout() {
  await fetch("/api/logout", { method: "POST", headers: authHeaders() });
  localStorage.removeItem("token");
  document.getElementById("authStatus").innerText = "Not logged in";
async function runLLM(task, extra = {}, outputId = "featureOutput") {
  if (!activeItem) return;
  const output = document.getElementById(outputId);
  output.innerText = "Thinking...";

  const res = await fetch("/api/llm", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      task,
      title: activeItem.title,
      date: activeItem.date,
      explanation: activeItem.explanation,
      ...extra,
    }),
  });
  const data = await res.json();
  const source = data.source === "llm" ? "LLM" : "fallback";
  output.innerText = `${data.response}\n\n[source: ${source}]`;
}

function generateRewrite(level) {
  runLLM("rewrite", { level }, "rewriteOutput");
}

function askImageQuestion() {
  const question = document.getElementById("chatQuestion").value;
  if (!question.trim()) return;
  runLLM("chat", { question }, "chatOutput");
}

function generateHotspots() {
  runLLM("hotspots");
}

function generateDidYouKnow() {
  runLLM("facts");
}

function generateSimilar() {
  runLLM("similar");
}

function generateTimelineContext() {
  runLLM("timeline");
}

function generateELI10() {
  runLLM("eli10", {}, "rewriteOutput");
}

function generateDistance() {
  const distance = document.getElementById("distanceInput").value;
  runLLM("distance", { distance });
}

function generateQuiz() {
  runLLM("quiz");
}

function generateStory() {
  runLLM("story");
}

function generateLesson() {
  runLLM("lesson");
}

function generateCaptions() {
  runLLM("captions");
}

function generateCollections() {
  if (!currentItems.length) return;
  activeItem = currentItems.find((i) => i.media_type === "image") || currentItems[0];
  runLLM("collections", {}, "collectionsOutput");
}

async function loadTimelineDate() {
  const year = document.getElementById("timelineYear").value;
  document.getElementById("timelineYearLabel").innerText = year;
  mode = "timeline";
  const items = await fetchApod(`?date=${year}-01-01`);
  currentItems = items;
  renderCards(items);
}

document.getElementById("timelineYear").addEventListener("input", (e) => {
  document.getElementById("timelineYearLabel").innerText = e.target.value;
});

function startVoiceQuestion() {
  const Recognition = window.SpeechRecognition || window.webkitSpeechRecognition;
  if (!Recognition) {
    document.getElementById("chatOutput").innerText = "Voice input is not supported in this browser.";
    return;
  }
  const recognition = new Recognition();
  recognition.lang = "en-US";
  recognition.onresult = (event) => {
    const transcript = event.results[0][0].transcript;
    document.getElementById("chatQuestion").value = transcript;
    askImageQuestion();
  };
  recognition.start();
}

loadMoreGallery();
loadFacts();
