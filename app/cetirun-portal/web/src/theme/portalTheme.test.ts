// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { describe, expect, it } from "vitest";
import { webDarkTheme, webLightTheme } from "@fluentui/react-components";
import { portalTheme, type PortalTheme } from "./portalTheme";

type Token = keyof PortalTheme;

function luminance(hex: string): number {
  const channels = [1, 3, 5].map((start) => {
    const value = parseInt(hex.slice(start, start + 2), 16) / 255;
    return value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2];
}

/** The WCAG contrast ratio of two opaque colors. */
function contrast(a: string, b: string): number {
  const [lighter, darker] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (lighter + 0.05) / (darker + 0.05);
}

const SURFACES: Token[] = ["colorNeutralBackground1", "colorNeutralBackground2", "portalWell"];
const TEXT: Token[] = ["colorNeutralForeground1", "colorNeutralForeground2", "colorNeutralForeground3", "colorBrandForegroundLink"];

// Text and the background it is drawn on, beyond plain text on a surface.
const TEXT_PAIRS: Array<[Token, Token]> = [
  ...TEXT.flatMap((text) => SURFACES.map((surface): [Token, Token] => [text, surface])),
  ["colorNeutralForegroundOnBrand", "colorBrandBackground"],
  ["colorNeutralForegroundOnBrand", "colorBrandBackgroundHover"],
  ["colorStatusDangerForeground1", "colorNeutralBackground1"],
  ["colorStatusWarningForeground2", "colorStatusWarningBackground1"],
  ["colorNeutralForeground1", "colorPaletteGreenBackground1"],
  ["colorNeutralForeground1", "colorPaletteDarkOrangeBackground1"],
  ["colorNeutralForeground1", "colorPaletteRedBackground1"],
  ["colorPaletteGreenForeground1", "colorPaletteGreenBackground1"],
  ["colorPaletteYellowForeground1", "colorPaletteYellowBackground1"],
  ["colorPaletteDarkOrangeForeground1", "colorPaletteDarkOrangeBackground1"],
  ["colorPaletteRedForeground1", "colorPaletteRedBackground1"],
];

const LAMPS: Token[] = ["portalLampReady", "portalLampBusy", "portalLampFault", "portalLampIdle"];
const DANGER: Token[] = ["colorStatusDangerBackground3", "colorStatusDangerBackground3Hover", "colorStatusDangerBackground3Pressed"];

describe("portalTheme", () => {
  it("draws dark paper for Fluent's dark theme and light paper otherwise", () => {
    expect(luminance(portalTheme(webDarkTheme).colorNeutralBackground2)).toBeLessThan(0.05);
    expect(luminance(portalTheme(webLightTheme).colorNeutralBackground2)).toBeGreaterThan(0.8);
  });

  describe.each([
    ["light", portalTheme(webLightTheme)],
    ["dark", portalTheme(webDarkTheme)],
  ])("%s", (_, theme) => {
    const color = (token: Token) => theme[token] as string;

    it.each(TEXT_PAIRS)("keeps %s readable on %s", (text, background) => {
      expect(contrast(color(text), color(background))).toBeGreaterThanOrEqual(4.5);
    });

    it.each(DANGER)("keeps white text readable on %s", (fill) => {
      expect(contrast("#ffffff", color(fill))).toBeGreaterThanOrEqual(4.5);
    });

    it.each(LAMPS)("keeps %s visible on a surface", (lamp) => {
      expect(contrast(color(lamp), color("colorNeutralBackground1"))).toBeGreaterThanOrEqual(3);
    });

    it.each([
      ["Green", "Success"],
      ["DarkOrange", "Warning"],
      ["Red", "Danger"],
    ])("gives %s badges and %s messages the same colors", (palette, status) => {
      for (const slot of ["Background1", "Foreground1", "Border1"]) {
        expect(color(`colorStatus${status}${slot}` as Token)).toBe(color(`colorPalette${palette}${slot}` as Token));
      }
    });

    it("tones every opaque gray", () => {
      const untoned = Object.entries(theme).filter(
        ([, value]) => typeof value === "string" && /^#([0-9a-f]{2})\1\1$/i.test(value) && !["#ffffff", "#000000"].includes(value),
      );
      expect(untoned).toEqual([]);
    });
  });
});
