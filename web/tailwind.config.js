/** @type {import('tailwindcss').Config} */

// Every colour points at a CSS variable declared in src/index.css, so a theme is
// just one block of variables under [data-theme="..."]. Keeping the palette keys
// that the components already use (slate/rose/emerald/amber) means switching
// themes needs no markup changes at all.
const v = (name) => `rgb(var(${name}) / <alpha-value>)`

export default {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  theme: {
    extend: {
      colors: {
        page: v('--page'),
        ink: v('--ink'),
        panel: v('--panel'),
        line: v('--line'),
        accent: v('--accent'),
        warm: v('--warm'),
        strong: v('--strong'),
        'on-accent': v('--on-accent'),
        slate: {
          100: v('--slate-100'),
          200: v('--slate-200'),
          300: v('--slate-300'),
          400: v('--slate-400'),
          500: v('--slate-500'),
          600: v('--slate-600'),
        },
        rose: {
          100: v('--rose-100'),
          200: v('--rose-200'),
          300: v('--rose-300'),
          500: v('--rose-500'),
          950: v('--rose-950'),
        },
        emerald: {
          100: v('--emerald-100'),
          200: v('--emerald-200'),
          300: v('--emerald-300'),
          400: v('--emerald-400'),
          500: v('--emerald-500'),
        },
        amber: {
          100: v('--amber-100'),
          200: v('--amber-200'),
          300: v('--amber-300'),
          400: v('--amber-400'),
          500: v('--amber-500'),
          950: v('--amber-950'),
        },
      },
      fontFamily: {
        sans: ['"Noto Sans SC"', 'system-ui', 'sans-serif'],
      },
    },
  },
  plugins: [],
}
