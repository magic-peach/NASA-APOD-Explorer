const gallery = document.getElementById("gallery");
const loader = document.getElementById("loader");
let currentItems = [];
let page = 0;
let isLoading = false;
let mode = "infinite";
let activeItem = null;

function setLoading(show) {
  loader.style.display = show ? "block" : "none";
}

async function fetchApod(params = "") {
  setLoading(true);
  try {
    const res = await fetch(`/api/apod${params}`);
    const data = await res.json();
    return Array.isArray(data) ? data : [data];
  } finally {
    setLoading(false);
  }
}

function renderCards(items, append = false) {
  if (!append) gallery.innerHTML = "";
  items
    .filter((item) => item.media_type === "image")
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

function saveFavorite() {
  const data = {
    title: document.getElementById("modalTitle").innerText,
    date: document.getElementById("modalDate").innerText,
    url: document.getElementById("modalImg").src,
  };

  let favs = JSON.parse(localStorage.getItem("favorites") || "[]");
  favs.push(data);

  localStorage.setItem("favorites", JSON.stringify(favs));
  alert("Saved to favorites!");
}

function showFavorites() {
  mode = "favorites";
  const favs = JSON.parse(localStorage.getItem("favorites") || "[]");
  renderCards(favs);
}

function toggleTheme() {
  document.body.classList.toggle("light");
}

async function loadMoreGallery() {
  if (isLoading || mode !== "infinite") return;
  isLoading = true;
  page += 1;
  const items = await fetchApod("?count=6");
  currentItems = currentItems.concat(items);
  renderCards(items, true);
  isLoading = false;
}

window.addEventListener("scroll", () => {
  if (
    window.innerHeight + window.scrollY >=
    document.body.offsetHeight - 200
  ) {
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
