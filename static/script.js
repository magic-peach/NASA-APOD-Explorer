const gallery = document.getElementById("gallery");
const loader = document.getElementById("loader");
let currentItems = [];
let page = 0;
let isLoading = false;
let mode = "infinite";

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

function openModal(data) {
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

loadMoreGallery();
loadFacts();
