export function gaugeGeometry(value, max = 100, size = 150) {
  const width = size;
  const height = size * 0.62;
  const centerX = width / 2;
  const centerY = height - 4;
  const radius = width / 2 - 12;
  const fraction = Math.max(0, Math.min(1, (Number(value) || 0) / max));
  const point = (f) => {
    const angle = Math.PI * (1 - f);
    return [centerX + radius * Math.cos(angle), centerY - radius * Math.sin(angle)];
  };
  return { width, height, centerX, centerY, radius, fraction, start: point(0), end: point(1), value: point(fraction), largeArcFlag: 0 };
}
