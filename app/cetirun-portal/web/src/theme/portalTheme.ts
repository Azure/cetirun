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
 * The Portal theme: paper and ink. Surfaces and text are neutral, primary
 * actions are ink, and one blue marks links and keyboard focus. Green, amber
 * and red are kept for the state of an Agent or of a request.
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

// Fluent derives the less common brand tokens from this ramp. It runs from
// ink to paper, so those tokens stay neutral too.
const inkRamp: BrandVariants = {
  10: "#080706",
  20: "#0f0e0c",
  30: "#171614",
  40: "#1d1c19",
  50: "#23211f",
  60: "#2b2a28",
  70: "#343330",
  80: "#3a3937",
  90: "#545250",
  100: "#747370",
  110: "#a09e9b",
  120: "#b9b7b4",
  130: "#cccac7",
  140: "#dad9d6",
  150: "#e8e6e3",
  160: "#f2f0ed",
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

/** The OKLCH color as sRGB hex. */
function oklch(lightness: number, chroma: number, hue: number): string {
  const a = chroma * Math.cos((hue * Math.PI) / 180);
  const b = chroma * Math.sin((hue * Math.PI) / 180);
  const l = (lightness + 0.3963377774 * a + 0.2158037573 * b) ** 3;
  const m = (lightness - 0.1055613458 * a - 0.0638541728 * b) ** 3;
  const s = (lightness - 0.0894841775 * a - 1.291485548 * b) ** 3;
  return `#${linearToChannel(4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s)}${linearToChannel(
    -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
  )}${linearToChannel(-0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s)}`;
}

const PURE_GRAY = /^#([0-9a-f]{2})\1\1$/i;

/**
 * Fluent's neutrals are pure grays. Give each the same faint warmth, and
 * move it along the lightness curve, so that every neutral token stays in
 * step with the few that are set by hand. A gray that the curve leaves at
 * full lightness stays white.
 */
function retone(theme: Theme, lightness: Lightness, chroma: number, hue: number): Theme {
  const toned: Record<string, unknown> = { ...theme };
  for (const [token, value] of Object.entries(theme)) {
    if (typeof value !== "string") continue;
    const gray = PURE_GRAY.exec(value);
    if (!gray) continue;
    // For a gray, OKLab lightness is the cube root of its linear luminance.
    const source = Math.cbrt(channelToLinear(parseInt(gray[1], 16)));
    const target = lightness(source);
    toned[token] = target >= 0.999 ? "#ffffff" : oklch(target, chroma, hue);
  }
  return toned as Theme;
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

const light: PortalTheme = {
  ...retone(createLightTheme(inkRamp), (l) => l, 0.0045, 82),
  ...shape,

  // Paper.
  colorNeutralBackground2: "#f7f5f2",
  colorNeutralBackground3: "#f2f0ed",
  colorNeutralBackground4: "#eceae7",
  colorNeutralBackground5: "#e6e4e1",
  colorNeutralBackground6: "#dfdedb",
  colorNeutralStroke1: "#d2d1ce",
  colorNeutralStroke2: "#e3e2df",
  colorNeutralStroke3: "#edebe8",
  colorNeutralStrokeAccessible: "#848380",
  colorNeutralStrokeAccessibleHover: "#666462",
  colorNeutralStrokeAccessiblePressed: "#545250",

  // Ink.
  colorNeutralForeground1: "#171614",
  colorNeutralForeground1Hover: "#171614",
  colorNeutralForeground1Pressed: "#171614",
  colorNeutralForeground1Selected: "#171614",
  colorNeutralForeground2: "#3a3937",
  colorNeutralForeground3: "#666462",
  colorNeutralForeground4: "#747370",
  colorNeutralForegroundDisabled: "#afaeab",

  // Primary actions and checked controls are ink.
  ...primaryColors("#171614", "#343330", "#080706"),
  colorBrandBackgroundSelected: "#23211f",
  colorBrandForeground2: "#3a3937",
  colorBrandForeground2Pressed: "#080706",
  colorBrandBackground2Pressed: "#dad9d6",

  // The accent.
  colorBrandForegroundLink: "#1b45c4",
  colorBrandForegroundLinkHover: "#14359c",
  colorBrandForegroundLinkPressed: "#0e2876",
  colorBrandForegroundLinkSelected: "#1b45c4",
  colorStrokeFocus2: "#1b45c4",

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

  // Elevation is for overlays only; the shadow is ink, not black.
  shadow2: "0 1px 2px rgba(23, 22, 20, 0.06)",
  shadow4: "0 1px 2px rgba(23, 22, 20, 0.05), 0 2px 6px rgba(23, 22, 20, 0.05)",
  shadow8: "0 2px 4px rgba(23, 22, 20, 0.05), 0 6px 16px rgba(23, 22, 20, 0.08)",
  shadow16: "0 2px 6px rgba(23, 22, 20, 0.06), 0 12px 28px rgba(23, 22, 20, 0.12)",
  shadow28: "0 4px 10px rgba(23, 22, 20, 0.06), 0 20px 44px rgba(23, 22, 20, 0.16)",
  shadow64: "0 6px 14px rgba(23, 22, 20, 0.08), 0 32px 72px rgba(23, 22, 20, 0.22)",
  colorBackgroundOverlay: "rgba(23, 22, 20, 0.36)",

  portalSelection: "#e3e9fb",
  portalWell: "#f2f0ed",
  portalHover: "#eceae7",
  portalLampReady: "#1f9d57",
  portalLampBusy: "#c57d00",
  portalLampFault: "#d93a2e",
  portalLampIdle: "#8f8d8a",
  portalFontDisplay: fontDisplay,
};

// Fluent's dark neutrals sit higher than the Portal's; the curve lowers them
// together so that hover, pressed and selected steps keep their order.
const darkLightness = curve([
  [0, 0.09],
  [0.145, 0.13],
  [0.19, 0.155],
  [0.24, 0.186],
  [0.283, 0.225],
  [0.32, 0.262],
  [0.36, 0.3],
  [0.44, 0.37],
  [0.51, 0.43],
  [0.75, 0.72],
  [0.87, 0.86],
  [1, 0.955],
]);

const dark: PortalTheme = {
  ...retone(createDarkTheme(inkRamp), darkLightness, 0.005, 82),
  ...shape,

  // Paper.
  colorNeutralBackground1: "#1d1c19",
  colorNeutralBackground2: "#141310",
  colorNeutralBackground3: "#0d0c0a",
  colorNeutralBackground6: "#262523",
  colorNeutralStroke1: "#4c4a47",
  colorNeutralStroke2: "#353431",
  colorNeutralStroke3: "#2b2a27",
  colorNeutralStrokeAccessible: "#8e8c89",
  colorNeutralStrokeAccessibleHover: "#a6a4a1",
  colorNeutralStrokeAccessiblePressed: "#bcbab7",

  // Ink.
  colorNeutralForeground1: "#f2f0ec",
  colorNeutralForeground1Hover: "#f2f0ec",
  colorNeutralForeground1Pressed: "#f2f0ec",
  colorNeutralForeground1Selected: "#f2f0ec",
  colorNeutralForeground2: "#d2d1cd",
  colorNeutralForeground3: "#a6a4a1",
  colorNeutralForeground4: "#8e8c89",
  colorNeutralForegroundDisabled: "#5f5d5a",
  colorNeutralForegroundOnBrand: "#141310",
  colorNeutralForegroundInverted: "#141310",
  colorNeutralForegroundInvertedHover: "#141310",
  colorNeutralForegroundInvertedPressed: "#141310",
  colorNeutralForegroundInvertedSelected: "#141310",

  // Primary actions invert: paper on ink.
  ...primaryColors("#f2f0ec", "#ffffff", "#c9c7c4"),
  colorBrandBackgroundSelected: "#e0deda",
  colorBrandForeground2: "#d2d1cd",
  colorBrandForeground2Pressed: "#ffffff",
  colorBrandStroke2: "#4c4a47",
  colorBrandStroke2Hover: "#666461",
  colorBrandStroke2Contrast: "#4c4a47",
  colorBrandBackground2: "#262523",
  colorBrandBackground2Hover: "#302f2c",
  colorBrandBackground2Pressed: "#1d1c19",

  colorBrandForegroundLink: "#9db4ff",
  colorBrandForegroundLinkHover: "#bccbff",
  colorBrandForegroundLinkPressed: "#8098ee",
  colorBrandForegroundLinkSelected: "#9db4ff",
  colorStrokeFocus1: "#141310",
  colorStrokeFocus2: "#9db4ff",

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

  portalSelection: "#27304f",
  portalWell: "#262523",
  portalHover: "#2b2a27",
  portalLampReady: "#3fc27e",
  portalLampBusy: "#f0aa2e",
  portalLampFault: "#ef5f52",
  portalLampIdle: "#7d7b78",
  portalFontDisplay: fontDisplay,
};

/** The Portal theme for a base Fluent theme: dark for Fluent's dark theme, light otherwise. */
export function portalTheme(base: Theme): PortalTheme {
  return base === webDarkTheme ? dark : light;
}
