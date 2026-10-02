import colors from "./colors.json";
import { useEffect, useState } from "react";
import {
  IconButton,
  ListItemIcon,
  ListItemText,
  Menu,
  MenuItem,
} from "@mui/material";
import { useColorScheme } from "@mui/material/styles";
import LightModeOutlined from "@mui/icons-material/LightModeOutlined";
import DarkModeOutlined from "@mui/icons-material/DarkModeOutlined";
import SettingsBrightnessOutlined from "@mui/icons-material/SettingsBrightnessOutlined";

const options = [
  { value: "system", label: "跟随系统", Icon: SettingsBrightnessOutlined },
  { value: "light", label: "浅色", Icon: LightModeOutlined },
  { value: "dark", label: "深色", Icon: DarkModeOutlined },
] as const;

export function AppearanceMenu() {
  const { mode, setMode, systemMode } = useColorScheme();
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  useEffect(() => {
    if (!mode) return;
    const dark =
      mode === "dark" || (mode === "system" && systemMode === "dark");
    document
      .querySelector('meta[name="theme-color"]')
      ?.setAttribute(
        "content",
        colors[dark ? "dark" : "light"].background.default,
      );
  }, [mode, systemMode]);
  function closeMenu() {
    const trigger = anchor;
    setAnchor(null);
    requestAnimationFrame(() => trigger?.focus({ preventScroll: true }));
  }
  const current = options.find((option) => option.value === mode) ?? options[0];
  return (
    <>
      <IconButton
        aria-label={`外观：${current.label}`}
        title={`外观：${current.label}`}
        aria-haspopup="menu"
        aria-expanded={!!anchor}
        aria-controls={anchor ? "appearance-menu" : undefined}
        onClick={(event) => setAnchor(event.currentTarget)}
        sx={{ color: "text.secondary", flexShrink: 0 }}
      >
        <current.Icon />
      </IconButton>
      <Menu
        // A small appearance menu must not hide the page scrollbar or scroll
        // the card when returning keyboard focus to its trigger.
        disableScrollLock
        disableRestoreFocus
        id="appearance-menu"
        anchorEl={anchor}
        open={!!anchor}
        onClose={closeMenu}
        slotProps={{ list: { "aria-label": "选择外观" } }}
      >
        {options.map(({ value, label, Icon }) => (
          <MenuItem
            key={value}
            selected={current.value === value}
            role="menuitemradio"
            aria-checked={current.value === value}
            onClick={() => {
              setMode(value);
              closeMenu();
            }}
            sx={{ minHeight: 44 }}
          >
            <ListItemIcon>
              <Icon fontSize="small" />
            </ListItemIcon>
            <ListItemText>{label}</ListItemText>
          </MenuItem>
        ))}
      </Menu>
    </>
  );
}
