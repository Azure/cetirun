// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import {
  createDarkTheme,
  createLightTheme,
  webDarkTheme,
  type BrandVariants,
  type Theme,
} from "@fluentui/react-components";

/**
 * The Portal theme. Surfaces and text are neutral: white, gray and near black
 * in the light theme, black and gray in the dark theme. Primary actions, links
 * and keyboard focus take the text color. Green, amber and red are kept for
 * the state of an Agent or of a request. The documentation site uses the same
 * colors (site/assets/scss/_styles_project.scss); change the two together.
 */

/** Tokens the Portal adds to the Fluent theme. FluentProvider publishes every theme key as a CSS variable. */
interface PortalTokens {
  /** Background of selected text. */
  portalSelection: string;
  /** Fill of a block that groups related content on a surface. */
  portalWell: string;
  /** Fill of a navigation entry or row under the pointer. */
  portalHover: string;
  /** Lamp colors of the lifecycle glyph and of status labels. */
  portalLampReady: string;
  portalLampBusy: string;
  portalLampFault: string;
  portalLampIdle: string;
  /** Headings use the display cut of the system font where it exists. */
  portalFontDisplay: string;
}

export type PortalTheme = Theme & PortalTokens;

const systemSans = 'system-ui, -apple-system, "Segoe UI", "Helvetica Neue", "Noto Sans", "Liberation Sans", Arial, sans-serif';
const fontBase = `"Segoe UI Variable Text", ${systemSans}`;
const fontDisplay = `"Segoe UI Variable Display", ${systemSans}`;
const fontMonospace =
  '"Cascadia Code", "Cascadia Mono", Consolas, "SF Mono", Menlo, "Noto Sans Mono", "DejaVu Sans Mono", "Liberation Mono", ui-monospace, monospace';

// Fluent derives the less common brand tokens from this ramp. It runs through
// pure grays, so those tokens stay neutral too.
const grayRamp: BrandVariants = {
  10: "#080808",
  20: "#0f0f0f",
  30: "#161616",
  40: "#1c1c1c",
  50: "#222222",
  60: "#2a2a2a",
  70: "#333333",
  80: "#3a3a3a",
  90: "#525252",
  100: "#737373",
  110: "#9e9e9e",
  120: "#b8b8b8",
  130: "#cacaca",
  140: "#d9d9d9",
  150: "#e6e6e6",
  160: "#f0f0f0",
};

type Lightness = (l: number) => number;

/** Linear interpolation through sorted [input, output] points. */
function curve(points: Array<[number, number]>): Lightness {
  return (l) => {
    const upper = points.findIndex(([input]) => input >= l);
    if (upper <= 0) return points[upper === 0 ? 0 : points.length - 1][1];
    const [x0, y0] = points[upper - 1];
    const [x1, y1] = points[upper];
    return y0 + ((l - x0) / (x1 - x0)) * (y1 - y0);
  };
}

const channelToLinear = (value: number) => {
  const c = value / 255;
  return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
};

const linearToChannel = (value: number) => {
  const c = value <= 0.0031308 ? 12.92 * value : 1.055 * value ** (1 / 2.4) - 0.055;
  return Math.round(Math.min(1, Math.max(0, c)) * 255)
    .toString(16)
    .padStart(2, "0");
};

const PURE_GRAY = /^#([0-9a-f]{2})\1\1$/i;

/** OKLab lightness of a pure gray: the cube root of its linear luminance. */
const lightnessOf = (gray: string) => Math.cbrt(channelToLinear(parseInt(gray.slice(1, 3), 16)));

/**
 * Moves each of Fluent's grays along the lightness curve, so that every
 * neutral token stays in step with the few that are set by hand.
 */
function relight(theme: Theme, lightness: Lightness): Theme {
  const relit: Record<string, unknown> = { ...theme };
  for (const [token, value] of Object.entries(theme)) {
    if (typeof value !== "string" || !PURE_GRAY.test(value)) continue;
    const channel = linearToChannel(lightness(lightnessOf(value)) ** 3);
    relit[token] = `#${channel}${channel}${channel}`;
  }
  return relit as Theme;
}

/** Primary actions and checked controls: one color at rest, one under the pointer, one when pressed. */
function primaryColors(rest: string, hover: string, pressed: string): Partial<Theme> {
  return {
    colorBrandBackground: rest,
    colorBrandBackgroundStatic: rest,
    colorCompoundBrandBackground: rest,
    colorCompoundBrandForeground1: rest,
    colorCompoundBrandStroke: rest,
    colorNeutralStrokeAccessibleSelected: rest,
    colorBrandForeground1: rest,
    colorBrandForeground2Hover: rest,
    colorNeutralForeground2BrandHover: rest,
    colorNeutralForeground2BrandSelected: rest,
    colorNeutralForeground3BrandHover: rest,
    colorNeutralForeground3BrandSelected: rest,
    colorBrandStroke1: rest,
    colorBrandStroke2Pressed: rest,
    colorBrandBackgroundHover: hover,
    colorCompoundBrandBackgroundHover: hover,
    colorCompoundBrandForeground1Hover: hover,
    colorCompoundBrandStrokeHover: hover,
    colorBrandBackgroundPressed: pressed,
    colorCompoundBrandBackgroundPressed: pressed,
    colorCompoundBrandForeground1Pressed: pressed,
    colorCompoundBrandStrokePressed: pressed,
    colorNeutralForeground2BrandPressed: pressed,
    colorNeutralForeground3BrandPressed: pressed,
  };
}

const STATE_SLOTS = ["Background1", "Background2", "Background3", "Foreground1", "Foreground2", "Foreground3", "Border1", "Border2", "BorderActive"] as const;

/**
 * Fluent reads the colors of a state under two names: colorPalette<palette>*
 * in Badge and Field, colorStatus<status>* in MessageBar. Both get one set.
 */
function stateColors(
  palette: "Green" | "DarkOrange" | "Red",
  status: "Success" | "Warning" | "Danger",
  colors: Record<(typeof STATE_SLOTS)[number], string>,
): Partial<Theme> {
  const tokens: Record<string, string> = {};
  for (const slot of STATE_SLOTS) {
    tokens[`colorPalette${palette}${slot}`] = colors[slot];
    tokens[`colorStatus${status}${slot}`] = colors[slot];
  }
  return tokens as Partial<Theme>;
}

const shape: Partial<Theme> = {
  fontFamilyBase: fontBase,
  fontFamilyNumeric: fontBase,
  fontFamilyMonospace: fontMonospace,
  borderRadiusSmall: "3px",
  borderRadiusMedium: "6px",
  borderRadiusLarge: "8px",
  borderRadiusXLarge: "12px",
};

const lightText = "#1a1a1a";
const darkText = "#e8e8e8";

const light: PortalTheme = {
  ...createLightTheme(grayRamp),
  ...shape,

  // The page is white, like a surface. A hairline separates the two.
  colorNeutralBackground2: "#ffffff",
  colorNeutralStrokeAccessible: "#858585",
  colorNeutralStrokeAccessibleHover: "#666666",
  colorNeutralStrokeAccessiblePressed: "#525252",

  // Text.
  colorNeutralForeground1: lightText,
  colorNeutralForeground1Hover: lightText,
  colorNeutralForeground1Pressed: lightText,
  colorNeutralForeground1Selected: lightText,
  colorNeutralForeground2: "#3d3d3d",
  colorNeutralForeground3: "#5d5d5d",
  colorNeutralForeground4: "#737373",
  colorNeutralForegroundDisabled: "#b0b0b0",

  // Primary actions, checked controls and links take the text color.
  ...primaryColors(lightText, "#333333", "#000000"),
  colorBrandBackgroundSelected: "#222222",
  colorBrandForeground2: "#3d3d3d",
  colorBrandForeground2Pressed: "#000000",
  colorBrandBackground2Pressed: "#d9d9d9",
  colorBrandForegroundLink: lightText,
  colorBrandForegroundLinkHover: "#000000",
  colorBrandForegroundLinkPressed: "#000000",
  colorBrandForegroundLinkSelected: lightText,

  // State.
  ...stateColors("Green", "Success", {
    Background1: "#eaf5ee", Background2: "#c3e3cf", Background3: "#1a7f4b",
    Foreground1: "#17693f", Foreground2: "#0f4d2d", Foreground3: "#17693f",
    Border1: "#c3e3cf", Border2: "#1a7f4b", BorderActive: "#1a7f4b",
  }),
  colorPaletteYellowBackground1: "#fbf3df",
  colorPaletteYellowBackground2: "#f4e1ae",
  colorPaletteYellowBackground3: "#edb234",
  colorPaletteYellowForeground1: "#7a4a00",
  colorPaletteYellowForeground2: "#7a4a00",
  colorPaletteYellowForeground3: "#96600a",
  colorPaletteYellowBorder1: "#efdba3",
  colorPaletteYellowBorder2: "#c98a12",
  colorPaletteYellowBorderActive: "#c98a12",
  ...stateColors("DarkOrange", "Warning", {
    Background1: "#fbf3df", Background2: "#f4e1ae", Background3: "#a35e00",
    Foreground1: "#8a5300", Foreground2: "#5f3900", Foreground3: "#8a5300",
    Border1: "#efdba3", Border2: "#a35e00", BorderActive: "#a35e00",
  }),
  ...stateColors("Red", "Danger", {
    Background1: "#fbeeec", Background2: "#f4cbc5", Background3: "#c4342a",
    Foreground1: "#a3241b", Foreground2: "#6e1710", Foreground3: "#b12a20",
    Border1: "#f1c8c2", Border2: "#c4342a", BorderActive: "#c4342a",
  }),
  colorStatusDangerBackground3Hover: "#a82b22",
  colorStatusDangerBackground3Pressed: "#8c231c",

  // Elevation is for overlays only.
  shadow2: "0 1px 2px rgba(0, 0, 0, 0.06)",
  shadow4: "0 1px 2px rgba(0, 0, 0, 0.05), 0 2px 6px rgba(0, 0, 0, 0.05)",
  shadow8: "0 2px 4px rgba(0, 0, 0, 0.05), 0 6px 16px rgba(0, 0, 0, 0.08)",
  shadow16: "0 2px 6px rgba(0, 0, 0, 0.06), 0 12px 28px rgba(0, 0, 0, 0.12)",
  shadow28: "0 4px 10px rgba(0, 0, 0, 0.06), 0 20px 44px rgba(0, 0, 0, 0.16)",
  shadow64: "0 6px 14px rgba(0, 0, 0, 0.08), 0 32px 72px rgba(0, 0, 0, 0.22)",
  colorBackgroundOverlay: "rgba(0, 0, 0, 0.36)",

  portalSelection: "#dcdcdc",
  portalWell: "#f3f3f3",
  portalHover: "#e9e9e9",
  portalLampReady: "#1f9d57",
  portalLampBusy: "#c57d00",
  portalLampFault: "#d93a2e",
  portalLampIdle: "#8f8f8f",
  portalFontDisplay: fontDisplay,
};

// Fluent's dark grays sit higher than the Portal's. Each row gives a gray of
// Fluent and the gray that takes its place. A gray between two rows keeps its
// place between them, so hover, pressed and selected steps keep their order.
const darkLightness = curve(
  [
    ["#000000", "#000000"],
    ["#0a0a0a", "#050505"],
    ["#141414", "#0a0a0a"],
    ["#1f1f1f", "#0d0d0d"],
    ["#292929", "#141414"],
    ["#333333", "#1c1c1c"],
    ["#3d3d3d", "#262626"],
    ["#525252", "#2e2e2e"],
    ["#666666", "#3a3a3a"],
    ["#adadad", "#a8a8a8"],
    ["#d6d6d6", "#c8c8c8"],
    ["#ffffff", darkText],
  ].map(([fluent, portal]): [number, number] => [lightnessOf(fluent), lightnessOf(portal)]),
);

const dark: PortalTheme = {
  ...relight(createDarkTheme(grayRamp), darkLightness),
  ...shape,

  // The page is black. The grays above put a surface one step higher.
  colorNeutralBackground2: "#000000",
  colorNeutralStroke2: "#262626",
  colorNeutralStroke3: "#1f1f1f",
  colorNeutralStrokeAccessible: "#8c8c8c",
  colorNeutralStrokeAccessibleHover: "#a6a6a6",
  colorNeutralStrokeAccessiblePressed: "#bababa",
  colorNeutralForegroundDisabled: "#5c5c5c",
  colorNeutralForegroundOnBrand: "#0d0d0d",

  // Primary actions invert: dark text on a light fill.
  ...primaryColors("#f3f3f3", "#ffffff", "#c9c9c9"),
  colorBrandBackgroundSelected: "#e0e0e0",
  colorBrandForeground2: "#c8c8c8",
  colorBrandForeground2Pressed: "#ffffff",
  colorBrandStroke2: "#3a3a3a",
  colorBrandStroke2Hover: "#666666",
  colorBrandStroke2Contrast: "#3a3a3a",
  colorBrandBackground2: "#1c1c1c",
  colorBrandBackground2Hover: "#2a2a2a",
  colorBrandBackground2Pressed: "#141414",
  colorBrandForegroundLink: darkText,
  colorBrandForegroundLinkHover: "#ffffff",
  colorBrandForegroundLinkPressed: "#c8c8c8",
  colorBrandForegroundLinkSelected: darkText,

  ...stateColors("Green", "Success", {
    Background1: "#14241b", Background2: "#1b3b2a", Background3: "#3fc27e",
    Foreground1: "#7fd8a6", Foreground2: "#a9e6c3", Foreground3: "#7fd8a6",
    Border1: "#234a35", Border2: "#3fc27e", BorderActive: "#3fc27e",
  }),
  colorPaletteYellowBackground1: "#272012",
  colorPaletteYellowBackground2: "#43340f",
  colorPaletteYellowBackground3: "#f0aa2e",
  colorPaletteYellowForeground1: "#f2c36b",
  colorPaletteYellowForeground2: "#f2c36b",
  colorPaletteYellowForeground3: "#f0aa2e",
  colorPaletteYellowBorder1: "#51400f",
  colorPaletteYellowBorder2: "#f0aa2e",
  colorPaletteYellowBorderActive: "#f0aa2e",
  ...stateColors("DarkOrange", "Warning", {
    Background1: "#272012", Background2: "#43340f", Background3: "#f0aa2e",
    Foreground1: "#f2c36b", Foreground2: "#f7dba0", Foreground3: "#f2c36b",
    Border1: "#51400f", Border2: "#f0aa2e", BorderActive: "#f0aa2e",
  }),
  ...stateColors("Red", "Danger", {
    Background1: "#2c1815", Background2: "#4d211c", Background3: "#ef5f52",
    Foreground1: "#f5958b", Foreground2: "#f9bdb6", Foreground3: "#f5958b",
    Border1: "#5c2620", Border2: "#ef5f52", BorderActive: "#ef5f52",
  }),
  // A destructive button carries white text, so its fill is deeper than the lamp red.
  colorStatusDangerBackground3: "#cf4034",
  colorStatusDangerBackground3Hover: "#bf392e",
  colorStatusDangerBackground3Pressed: "#a93127",

  shadow2: "0 1px 2px rgba(0, 0, 0, 0.3)",
  shadow4: "0 1px 2px rgba(0, 0, 0, 0.3), 0 2px 6px rgba(0, 0, 0, 0.24)",
  shadow8: "0 2px 4px rgba(0, 0, 0, 0.3), 0 6px 16px rgba(0, 0, 0, 0.3)",
  shadow16: "0 2px 6px rgba(0, 0, 0, 0.32), 0 12px 28px rgba(0, 0, 0, 0.4)",
  shadow28: "0 4px 10px rgba(0, 0, 0, 0.34), 0 20px 44px rgba(0, 0, 0, 0.46)",
  shadow64: "0 6px 14px rgba(0, 0, 0, 0.36), 0 32px 72px rgba(0, 0, 0, 0.56)",
  colorBackgroundOverlay: "rgba(0, 0, 0, 0.56)",

  portalSelection: "#404040",
  portalWell: "#1c1c1c",
  portalHover: "#2a2a2a",
  portalLampReady: "#3fc27e",
  portalLampBusy: "#f0aa2e",
  portalLampFault: "#ef5f52",
  portalLampIdle: "#7a7a7a",
  portalFontDisplay: fontDisplay,
};

/** The Portal theme for a base Fluent theme: dark for Fluent's dark theme, light otherwise. */
export function portalTheme(base: Theme): PortalTheme {
  return base === webDarkTheme ? dark : light;
}
