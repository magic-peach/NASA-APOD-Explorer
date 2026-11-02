// Thin client for the server's JSON API. Every call either resolves with
// data or rejects with an ApiError carrying the server's error code and a
// human-readable message, so the UI can show exactly what went wrong.

export class ApiError extends Error {
  constructor(status, code, message) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

async function request(path, { method = "GET", body } = {}) {
  const init = { method, headers: {} };
  if (body !== undefined) {
    init.headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(body);
  }

  let res;
  try {
    // Same-origin fetches send the HttpOnly session cookie automatically.
    res = await fetch(path, init);
  } catch {
    throw new ApiError(0, "network", "Can't reach the server. Check your connection and try again.");
  }

  if (res.status === 204) return null;
  const data = await res.json().catch(() => null);
  if (!res.ok) {
    const err = data?.error;
    throw new ApiError(res.status, err?.code ?? "unknown", err?.message ?? `The request failed (${res.status}).`);
  }
  return data;
}

const query = (params) => `?${new URLSearchParams(params)}`;

export const api = {
  today: () => request("/api/apod"),
  day: (date) => request(`/api/apod${query({ date })}`),
  range: (start, end) => request(`/api/apod${query({ start_date: start, end_date: end })}`),
  random: (count) => request(`/api/apod${query({ count })}`),
  onThisDay: (date, years = 6) => request(`/api/apod/on-this-day${query({ date, years })}`),
  facts: () => request("/api/space-facts"),

  me: () => request("/api/auth/me"),
  signup: (username, email, password) => request("/api/auth/signup", { method: "POST", body: { username, email, password } }),
  login: (username, password) => request("/api/auth/login", { method: "POST", body: { username, password } }),
  logout: () => request("/api/auth/logout", { method: "POST" }),

  favorites: () => request("/api/favorites"),
  addFavorite: (fav) => request("/api/favorites", { method: "POST", body: { date: fav.date, title: fav.title, url: fav.url } }),
  removeFavorite: (date) => request(`/api/favorites/${encodeURIComponent(date)}`, { method: "DELETE" }),
  history: () => request("/api/history"),
  recordView: (item) => request("/api/history", { method: "POST", body: { date: item.date, title: item.title } }),

  comments: (date) => request(`/api/comments${query({ date })}`),
  addComment: (date, comment) => request(`/api/comments${query({ date })}`, { method: "POST", body: { comment } }),
  rating: (date) => request(`/api/ratings${query({ date })}`),
  rate: (date, rating) => request(`/api/ratings${query({ date })}`, { method: "POST", body: { rating } }),

  explain: (task, item, extra = {}) =>
    request("/api/llm", {
      method: "POST",
      body: { task, title: item.title, date: item.date, explanation: item.explanation ?? "", ...extra },
    }),
};
