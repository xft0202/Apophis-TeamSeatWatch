import { useEffect, useRef } from 'react';

export default function LoginDotGrid() {
  const canvas = useRef<HTMLCanvasElement>(null);

  useEffect(() => {
    const element = canvas.current;
    if (!element) return;
    const context = element.getContext('2d');
    if (!context) return;
    let pointer: { x: number; y: number } | null = null;
    let frame = 0;
    const draw = () => {
      const { width, height } = element.getBoundingClientRect();
      const ratio = window.devicePixelRatio || 1;
      element.width = Math.round(width * ratio);
      element.height = Math.round(height * ratio);
      context.scale(ratio, ratio);
      context.fillStyle = getComputedStyle(element).color;
      for (let x = 16; x < width; x += 24) {
        for (let y = 16; y < height; y += 24) {
          const proximity = pointer ? Math.max(0, 1 - Math.hypot(x - pointer.x, y - pointer.y) / 120) : 0;
          context.globalAlpha = 0.16 + proximity * 0.48;
          context.beginPath();
          context.arc(x, y, 1 + proximity * 1.5, 0, Math.PI * 2);
          context.fill();
        }
      }
    };
    const schedule = () => { cancelAnimationFrame(frame); frame = requestAnimationFrame(draw); };
    const move = (event: PointerEvent) => {
      const rect = element.getBoundingClientRect();
      pointer = { x: event.clientX - rect.left, y: event.clientY - rect.top };
      schedule();
    };
    const leave = () => { pointer = null; schedule(); };
    const resize = new ResizeObserver(schedule);
    const theme = new MutationObserver(schedule);
    resize.observe(element);
    theme.observe(document.documentElement, { attributes: true, attributeFilter: ['data-mantine-color-scheme'] });
    element.addEventListener('pointermove', move);
    element.addEventListener('pointerleave', leave);
    schedule();
    return () => {
      cancelAnimationFrame(frame);
      resize.disconnect();
      theme.disconnect();
      element.removeEventListener('pointermove', move);
      element.removeEventListener('pointerleave', leave);
    };
  }, []);

  return <canvas ref={canvas} className="auth-dot-grid" aria-hidden="true" />;
}
