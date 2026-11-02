// Turning APOD entries into images, video players and thumbnails.
import { h } from "./dom.js";

/** Only http(s) URLs are used; anything else (javascript:, data:) is dropped. */
export function safeUrl(url) {
  try {
    const u = new URL(url, location.href);
    return u.protocol === "https:" || u.protocol === "http:" ? u.href : "";
  } catch {
    return "";
  }
}

/**
 * classifyVideo works out how to play an APOD video URL. APOD videos are
 * usually YouTube or Vimeo embeds, occasionally a direct file.
 * YouTube is played from youtube-nocookie.com, its privacy-enhanced domain.
 */
export function classifyVideo(rawUrl) {
  const url = safeUrl(rawUrl);
  if (!url) return { kind: "none" };
  const u = new URL(url);
  const host = u.hostname.replace(/^www\./, "");

  let id = "";
  if (host === "youtu.be") id = u.pathname.slice(1);
  else if (host.endsWith("youtube.com") || host.endsWith("youtube-nocookie.com")) {
    id = u.searchParams.get("v") || u.pathname.match(/\/(?:embed|shorts)\/([^/?]+)/)?.[1] || "";
  }
  if (/^[\w-]{11}$/.test(id)) {
    return { kind: "iframe", src: `https://www.youtube-nocookie.com/embed/${id}?rel=0` };
  }

  const vimeo = (host === "vimeo.com" || host === "player.vimeo.com") && u.pathname.match(/(\d{5,})/)?.[1];
  if (vimeo) return { kind: "iframe", src: `https://player.vimeo.com/video/${vimeo}` };

  if (/\.(mp4|webm|ogv|mov)$/i.test(u.pathname)) return { kind: "file", src: url };
  return { kind: "link", src: url };
}

/** The image to show on a card: the picture itself, or a video's poster. */
export function thumbnailOf(item) {
  if (item.media_type === "video") return safeUrl(item.thumbnail_url ?? "");
  return safeUrl(item.url ?? "");
}

/** An <img> that fades in once loaded instead of popping in. */
export function fadeImage(src, alt, attrs = {}) {
  const img = h("img", { src, alt, decoding: "async", ...attrs, class: `fade ${attrs.class ?? ""}`.trim() });
  const done = () => img.classList.add("is-loaded");
  if (img.complete && img.naturalWidth) done();
  img.addEventListener("load", done, { once: true });
  img.addEventListener("error", () => img.classList.add("is-broken"), { once: true });
  return img;
}

/** The full-size media for the viewer. */
export function buildMedia(item, { hd = false } = {}) {
  if (item.media_type === "video") {
    const video = classifyVideo(item.url);
    const poster = safeUrl(item.thumbnail_url ?? "");
    switch (video.kind) {
      case "iframe":
        return h("iframe", {
          src: video.src,
          title: `Video: ${item.title}`,
          allow: "fullscreen; picture-in-picture",
          allowfullscreen: true,
          referrerpolicy: "strict-origin-when-cross-origin",
          loading: "lazy",
        });
      case "file":
        return h("video", { src: video.src, controls: true, preload: "metadata", playsinline: true, poster: poster || false },
          "Your browser can't play this video. ", h("a", { href: video.src }, "Download it instead."));
      case "link":
        return h("div", { class: "media-fallback" },
          poster ? fadeImage(poster, item.title) : null,
          h("a", { class: "button", href: video.src, target: "_blank", rel: "noopener" }, "Watch this video on its original site"));
      default:
        return h("p", { class: "media-fallback" }, "This entry's video is unavailable.");
    }
  }
  const src = safeUrl(hd && item.hdurl ? item.hdurl : item.url);
  return src ? fadeImage(src, item.title) : h("p", { class: "media-fallback" }, "This entry's image is unavailable.");
}
