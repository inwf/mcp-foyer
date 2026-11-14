import { theme, type ThemeConfig } from 'antd';

// antd derives its component colours at module load, so these mirror the
// CSS palette rather than reading computed styles during a render.
interface Palette {
  void: string;
  surface: string;
  raised: string;
  overlay: string;
  accent: string;
  accentStrong: string;
  accentDim: string;
  ink: string;
  inkMuted: string;
  inkFaint: string;
  inkInverse: string;
  line: string;
  lineStrong: string;
  hover: string;
  ok: string;
  warn: string;
  danger: string;
  info: string;
}

const LIGHT: Palette = {
  void: '#faf9f6',
  surface: '#ffffff',
  raised: '#f5f4f0',
  overlay: '#ffffff',
  accent: '#a4492d',
  accentStrong: '#84371f',
  accentDim: '#f4e8e2',
  ink: '#282824',
  inkMuted: '#686860',
  inkFaint: '#6d6c63',
  inkInverse: '#ffffff',
  line: '#e6e4df',
  lineStrong: '#d2cfc7',
  hover: '#f7f6f3',
  ok: '#38734d',
  warn: '#916019',
  danger: '#b44040',
  info: '#526d87',
};

const DARK: Palette = {
  void: '#1d1e1c',
  surface: '#232421',
  raised: '#292a26',
  overlay: '#2c2d29',
  accent: '#df9b7f',
  accentStrong: '#edb69d',
  accentDim: '#3c2e27',
  ink: '#e9e9e1',
  inkMuted: '#acaea2',
  inkFaint: '#929589',
  inkInverse: '#1c1d19',
  line: '#373832',
  lineStrong: '#4b4d44',
  hover: '#292a26',
  ok: '#9ac6a0',
  warn: '#d9b873',
  danger: '#e8958f',
  info: '#a4b9cd',
};

function configFor(p: Palette, dark: boolean): ThemeConfig {
  return {
    algorithm: dark ? theme.darkAlgorithm : theme.defaultAlgorithm,
    token: {
      colorPrimary: p.accent,
      colorPrimaryActive: p.accentStrong,
      colorInfo: p.info,
      colorSuccess: p.ok,
      colorWarning: p.warn,
      colorError: p.danger,
      colorLink: p.accent,
      colorLinkHover: p.accentStrong,
      colorBgBase: p.void,
      colorBgLayout: p.void,
      colorBgContainer: p.surface,
      colorBgElevated: p.overlay,
      colorBgSpotlight: p.overlay,
      colorBorder: p.lineStrong,
      colorBorderSecondary: p.line,
      colorSplit: p.line,
      colorTextBase: p.ink,
      colorText: p.ink,
      colorTextSecondary: p.inkMuted,
      colorTextTertiary: p.inkFaint,
      colorTextQuaternary: p.inkFaint,
      colorTextPlaceholder: p.inkFaint,
      fontFamily:
        "-apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC', 'Hiragino Sans GB', 'Microsoft YaHei', sans-serif",
      fontFamilyCode: "'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, monospace",
      fontSize: 14,
      borderRadius: 8,
      borderRadiusLG: 12,
      borderRadiusSM: 4,
      controlHeight: 36,
      controlHeightSM: 28,
      fontWeightStrong: 500,
      lineWidth: 1,
      motionDurationMid: '0.12s',
      motionDurationSlow: '0.2s',
      motionEaseInOut: 'cubic-bezier(0.22, 1, 0.36, 1)',
      motionEaseOut: 'cubic-bezier(0.22, 1, 0.36, 1)',
      motionEaseOutCirc: 'cubic-bezier(0.22, 1, 0.36, 1)',
      motionEaseOutBack: 'cubic-bezier(0.22, 1, 0.36, 1)',
    },
    components: {
      Table: {
        headerBg: p.raised,
        headerColor: p.inkMuted,
        headerSplitColor: 'transparent',
        headerBorderRadius: 0,
        borderColor: p.line,
        rowHoverBg: p.hover,
        rowSelectedBg: p.accentDim,
        rowSelectedHoverBg: p.accentDim,
        cellPaddingBlock: 16,
        cellPaddingInline: 20,
        fontSize: 14,
        footerBg: p.raised,
      },
      Form: {
        labelColor: p.inkMuted,
        labelFontSize: 14,
        verticalLabelPadding: '0 0 8px',
        itemMarginBottom: 24,
      },
      Layout: { bodyBg: p.void, headerBg: p.surface, siderBg: p.raised },
      Menu: {
        itemBg: 'transparent',
        subMenuItemBg: 'transparent',
        itemSelectedBg: p.accentDim,
        itemSelectedColor: p.accent,
        itemHoverBg: p.hover,
        itemBorderRadius: 8,
      },
      Card: { colorBgContainer: p.surface, headerFontSize: 14, paddingLG: 24 },
      Modal: { contentBg: p.overlay, headerBg: p.overlay, titleFontSize: 18 },
      Drawer: { colorBgElevated: p.surface, paddingLG: 24 },
      Input: { activeShadow: `0 0 0 2px ${p.accentDim}` },
      InputNumber: { activeShadow: `0 0 0 2px ${p.accentDim}` },
      Select: { optionSelectedBg: p.accentDim },
      Button: {
        primaryColor: p.inkInverse,
        primaryShadow: 'none',
        defaultShadow: 'none',
        dangerShadow: 'none',
        fontWeight: 500,
        borderRadiusSM: 8,
      },
      Tabs: {
        itemColor: p.inkMuted,
        itemSelectedColor: p.ink,
        inkBarColor: p.accent,
        horizontalMargin: '0 0 24px 0',
      },
      Tag: { defaultBg: p.raised, defaultColor: p.inkMuted, borderRadiusSM: 4 },
      Tooltip: { colorBgSpotlight: p.overlay, colorTextLightSolid: p.ink },
      Segmented: {
        itemColor: p.inkMuted,
        itemSelectedBg: p.surface,
        itemSelectedColor: p.ink,
        trackBg: p.raised,
      },
      Empty: { colorTextDescription: p.inkFaint },
      Divider: { colorSplit: p.line },
      Descriptions: { labelBg: p.raised, titleColor: p.ink },
      Statistic: { contentFontSize: 28, titleFontSize: 13 },
      Popover: { colorBgElevated: p.overlay },
      Dropdown: { colorBgElevated: p.overlay },
      Message: { colorBgElevated: p.overlay },
      Notification: { colorBgElevated: p.overlay },
    },
  };
}

const THEMES = { light: configFor(LIGHT, false), dark: configFor(DARK, true) } as const;

export function antdThemeFor(mode: keyof typeof THEMES): ThemeConfig {
  return THEMES[mode];
}
