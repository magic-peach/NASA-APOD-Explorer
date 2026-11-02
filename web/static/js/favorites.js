// Favorites live on the server when you're logged in and in localStorage
// when you're not. Logging in moves any local favorites up to your account,
// so nothing saved while logged out is lost.

import { storage } from "./dom.js";
import { thumbnailOf } from "./media.js";

const LOCAL_KEY = "apod.favorites.v1";

export class Favorites {
  constructor(api) {
    this.api = api;
    this.loggedIn = false;
    this.byDate = new Map(); // date -> { date, title, url, saved_at }
  }

  /** Reload from the right source for the current login state. */
  async load(loggedIn) {
    this.loggedIn = loggedIn;
    const list = loggedIn ? await this.api.favorites() : storage.get(LOCAL_KEY, []);
    this.byDate = new Map(list.map((f) => [f.date, f]));
  }

  has(date) {
    return this.byDate.has(date);
  }

  get count() {
    return this.byDate.size;
  }

  /** Favorites, most recently saved first. */
  list() {
    return [...this.byDate.values()].sort((a, b) => String(b.saved_at).localeCompare(String(a.saved_at)));
  }

  async add(item) {
    const fav = {
      date: item.date,
      title: item.title,
      url: thumbnailOf(item) || item.url,
      saved_at: new Date().toISOString(),
    };
    if (this.loggedIn) await this.api.addFavorite(fav);
    this.byDate.set(fav.date, fav);
    this.saveLocal();
  }

  async remove(date) {
    if (this.loggedIn) {
      try {
        await this.api.removeFavorite(date);
      } catch (err) {
        if (err.status !== 404) throw err; // already gone is fine
      }
    }
    this.byDate.delete(date);
    this.saveLocal();
  }

  /** Toggle and report the new state. */
  async toggle(item) {
    if (this.has(item.date)) {
      await this.remove(item.date);
      return false;
    }
    await this.add(item);
    return true;
  }

  /**
   * After logging in: upload favorites saved while logged out, then switch
   * to the server's list. The server's POST is an upsert, so re-sending a
   * date it already has is harmless. Returns how many were uploaded.
   */
  async syncAfterLogin() {
    const local = storage.get(LOCAL_KEY, []);
    let uploaded = 0;
    for (const fav of local) {
      try {
        await this.api.addFavorite(fav);
        uploaded++;
      } catch {
        /* skip entries the server rejects; keep going */
      }
    }
    storage.remove(LOCAL_KEY);
    await this.load(true);
    return uploaded;
  }

  saveLocal() {
    if (!this.loggedIn) storage.set(LOCAL_KEY, [...this.byDate.values()]);
  }
}
