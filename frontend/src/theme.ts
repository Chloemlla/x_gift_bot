import { createTheme } from "@mui/material/styles";

// M3 tonal roles: blue primary, pink secondary, neutral gray surfaces.
// Status colors retain their semantic meaning in both schemes.
export const theme = createTheme({
  cssVariables: { colorSchemeSelector: "data" },
  colorSchemes: {
    light: {
      palette: {
        primary: {
          main: "#415F91",
          light: "#D6E3FF",
          dark: "#284777",
          contrastText: "#FFFFFF",
        },
        secondary: {
          main: "#875078",
          light: "#FFD7EF",
          dark: "#6C395F",
          contrastText: "#FFFFFF",
        },
        background: { default: "#F5F5F7", paper: "#FDFBFF" },
        text: { primary: "#1B1B1F", secondary: "#45464F" },
        divider: "#C5C6D0",
        success: { main: "#326B43", contrastText: "#FFFFFF" },
        warning: { main: "#805600", contrastText: "#FFFFFF" },
        error: { main: "#BA1A1A", contrastText: "#FFFFFF" },
        info: { main: "#415F91", contrastText: "#FFFFFF" },
      },
    },
    dark: {
      palette: {
        primary: {
          main: "#AAC7FF",
          light: "#D6E3FF",
          dark: "#7BA4DF",
          contrastText: "#0A305F",
        },
        secondary: {
          main: "#FAAFE0",
          light: "#FFD7EF",
          dark: "#DB94C3",
          contrastText: "#511F48",
        },
        background: { default: "#111318", paper: "#1D2026" },
        text: { primary: "#E3E2E9", secondary: "#C5C6D0" },
        divider: "#44464F",
        success: { main: "#99D5A5", contrastText: "#003917" },
        warning: { main: "#F3BF61", contrastText: "#432C00" },
        error: { main: "#FFB4AB", contrastText: "#690005" },
        info: { main: "#AAC7FF", contrastText: "#0A305F" },
      },
    },
  },
  shape: { borderRadius: 12 },
  spacing: 8,
  typography: {
    fontFamily:
      '-apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif',
    h1: {
      fontSize: "2.25rem",
      fontWeight: 650,
      lineHeight: 1.3,
      letterSpacing: "-0.025em",
    },
    h2: { fontSize: "1.5rem", fontWeight: 650, lineHeight: 1.4 },
    h3: { fontSize: "1.125rem", fontWeight: 650 },
    button: { fontWeight: 600, textTransform: "none" },
    body1: { lineHeight: 1.75 },
    body2: { lineHeight: 1.65 },
  },
  components: {
    MuiButton: {
      defaultProps: { disableElevation: true },
      styleOverrides: {
        root: { borderRadius: 24, minHeight: 44, paddingInline: 24 },
      },
    },
    MuiIconButton: {
      styleOverrides: { root: { minWidth: 44, minHeight: 44 } },
    },
    MuiPaper: {
      defaultProps: { elevation: 0 },
      styleOverrides: { root: { backgroundImage: "none" } },
    },
    MuiTextField: { defaultProps: { fullWidth: true, variant: "outlined" } },
    MuiOutlinedInput: {
      styleOverrides: {
        root: { borderRadius: 12 },
        notchedOutline: ({ theme }) => ({
          borderColor: theme.vars.palette.text.secondary,
        }),
      },
    },
    MuiInputBase: {
      styleOverrides: {
        input: ({ theme }) => ({
          "&::placeholder": {
            color: theme.vars.palette.text.secondary,
            opacity: 1,
          },
        }),
      },
    },
    MuiFormHelperText: {
      styleOverrides: {
        root: ({ theme }) => ({
          fontSize: ".8125rem",
          "&.Mui-disabled": { color: theme.vars.palette.text.secondary },
        }),
      },
    },
    MuiStepIcon: {
      styleOverrides: {
        root: ({ theme }) => ({ color: theme.vars.palette.text.secondary }),
        text: ({ theme }) => ({ fill: theme.vars.palette.background.paper }),
      },
    },
    MuiChip: {
      styleOverrides: {
        root: {
          fontWeight: 600,
          maxWidth: "100%",
          height: "auto",
          minHeight: 32,
        },
        label: { whiteSpace: "normal", paddingBlock: 4 },
      },
    },
    MuiAlert: {
      styleOverrides: {
        root: { borderRadius: 12 },
        message: { minWidth: 0, overflowWrap: "anywhere" },
      },
    },
    MuiCssBaseline: {
      styleOverrides: (theme) => ({
        body: { minWidth: 320, overflowWrap: "anywhere" },
        ":focus-visible": {
          outline: `3px solid ${theme.vars.palette.primary.main}`,
          outlineOffset: 3,
        },
        "@media (prefers-reduced-motion: reduce)": {
          html: { scrollBehavior: "auto" },
          ".MuiLinearProgress-bar, .MuiTouchRipple-child, .MuiCircularProgress-root":
            { animation: "none", transition: "none" },
        },
      }),
    },
  },
});
