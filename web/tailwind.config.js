/** @type {import('tailwindcss').Config} */
export default {
  darkMode: "class",
  content: ["./index.html", "./src/**/*.{svelte,js}"],
  theme: {
    extend: {
      // Colors resolve from CSS variables (see app.css), so light/dark is a swap
      // of :root values. Space-separated RGB channels keep Tailwind's opacity
      // modifiers (e.g. bg-panel/50, bg-black/60) working.
      colors: {
        bg: "rgb(var(--c-bg) / <alpha-value>)",
        panel: "rgb(var(--c-panel) / <alpha-value>)",
        panel2: "rgb(var(--c-panel2) / <alpha-value>)",
        border: "rgb(var(--c-border) / <alpha-value>)",
        accent: "rgb(var(--c-accent) / <alpha-value>)",
        accent2: "rgb(var(--c-accent2) / <alpha-value>)",
        muted: "rgb(var(--c-muted) / <alpha-value>)",
        text: "rgb(var(--c-text) / <alpha-value>)",
        danger: "rgb(var(--c-danger) / <alpha-value>)",
        warn: "rgb(var(--c-warn) / <alpha-value>)",
      },
      fontFamily: {
        mono: ["ui-monospace", "SFMono-Regular", "Menlo", "monospace"],
      },
    },
  },
  plugins: [],
};
