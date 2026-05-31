/** @type {import('tailwindcss').Config} */
export default {
  darkMode: "class",
  content: ["./index.html", "./src/**/*.{svelte,js}"],
  theme: {
    extend: {
      colors: {
        bg: "#0b0f14",
        panel: "#11161d",
        panel2: "#161b22",
        border: "#222a35",
        accent: "#3fb950",
        accent2: "#2ea043",
        muted: "#8b949e",
        text: "#e6edf3",
        danger: "#f85149",
        warn: "#d29922",
      },
      fontFamily: {
        mono: ["ui-monospace", "SFMono-Regular", "Menlo", "monospace"],
      },
    },
  },
  plugins: [],
};
