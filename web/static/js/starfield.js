// Animated starfield background on a <canvas>.
//
// Three depth layers drift at different speeds (parallax) and twinkle.
// With prefers-reduced-motion the field is drawn once and stays still.
// Animation pauses while the tab is hidden, so it costs nothing in the
// background.

const LAYERS = [
  { density: 0.00018, size: [0.4, 0.9], speed: 0.006, alpha: 0.55 }, // far
  { density: 0.00008, size: [0.8, 1.4], speed: 0.014, alpha: 0.75 }, // middle
  { density: 0.00002, size: [1.3, 2.0], speed: 0.03, alpha: 0.95 }, // near
];

// A few stars are tinted with the narrowband palette used elsewhere in the
// design (hydrogen-alpha rose, oxygen-III teal); most are white.
const TINTS = ["255,255,255", "255,255,255", "255,255,255", "255,214,222", "200,244,240"];

export function startStarfield(canvas) {
  const ctx = canvas.getContext("2d");
  if (!ctx) return;

  const motion = window.matchMedia("(prefers-reduced-motion: reduce)");
  let stars = [];
  let width = 0;
  let height = 0;
  let frame = 0;
  let last = 0;

  function resize() {
    const dpr = Math.min(window.devicePixelRatio || 1, 2); // cap for performance
    width = window.innerWidth;
    height = window.innerHeight;
    canvas.width = Math.round(width * dpr);
    canvas.height = Math.round(height * dpr);
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);

    const area = width * height;
    stars = LAYERS.flatMap((layer) =>
      Array.from({ length: Math.round(area * layer.density) }, () => ({
        x: Math.random() * width,
        y: Math.random() * height,
        r: layer.size[0] + Math.random() * (layer.size[1] - layer.size[0]),
        speed: layer.speed,
        alpha: layer.alpha,
        phase: Math.random() * Math.PI * 2,
        tint: TINTS[Math.floor(Math.random() * TINTS.length)],
      })),
    );
    draw(0);
  }

  function draw(time) {
    ctx.clearRect(0, 0, width, height);
    for (const s of stars) {
      const twinkle = motion.matches ? 1 : 0.75 + 0.25 * Math.sin(time * 0.0012 + s.phase);
      ctx.fillStyle = `rgba(${s.tint},${(s.alpha * twinkle).toFixed(3)})`;
      ctx.beginPath();
      ctx.arc(s.x, s.y, s.r, 0, Math.PI * 2);
      ctx.fill();
    }
  }

  function tick(time) {
    const dt = last ? Math.min(time - last, 50) : 16; // clamp after tab switches
    last = time;
    for (const s of stars) {
      s.x -= s.speed * dt; // slow drift to the left
      if (s.x < -2) s.x = width + 2;
    }
    draw(time);
    frame = requestAnimationFrame(tick);
  }

  function start() {
    cancelAnimationFrame(frame);
    last = 0;
    if (motion.matches || document.hidden) {
      draw(0); // static field
      return;
    }
    frame = requestAnimationFrame(tick);
  }

  let resizeTimer;
  window.addEventListener("resize", () => {
    clearTimeout(resizeTimer);
    resizeTimer = setTimeout(resize, 150);
  });
  document.addEventListener("visibilitychange", start);
  motion.addEventListener("change", start);

  resize();
  start();
}
