import React, { useEffect, useMemo, useState } from "react";
import { createRoot } from "react-dom/client";
import CssBaseline from "@mui/material/CssBaseline";
import { ThemeProvider, createTheme } from "@mui/material/styles";

import App from "./App.jsx";

const theme = createTheme({
  palette: {
    mode: "dark",
    primary: { main: "#58a6ff" },
    success: { main: "#3fb950" },
    warning: { main: "#d29922" },
    error: { main: "#f85149" },
    background: { default: "#0d1117", paper: "#161b22" },
    divider: "#21262d",
  },
  shape: { borderRadius: 10 },
  typography: {
    fontFamily:
      '-apple-system, BlinkMacSystemFont, "Segoe UI", "Noto Sans SC", Roboto, sans-serif',
    h6: { fontWeight: 600, letterSpacing: 0.2 },
  },
  components: {
    MuiPaper: { styleOverrides: { root: { backgroundImage: "none" } } },
    MuiCard: { defaultProps: { elevation: 0, variant: "outlined" } },
    MuiButton: { defaultProps: { disableElevation: true } },
  },
});

createRoot(document.getElementById("root")).render(
  <ThemeProvider theme={theme}>
    <CssBaseline />
    <App />
  </ThemeProvider>,
);

// Expose hooks used by App.jsx without a second entry point.
export { useEffect, useMemo, useState };
