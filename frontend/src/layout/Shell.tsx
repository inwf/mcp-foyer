import { NavLink, Outlet, useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Tooltip } from 'antd';
import {
  AppstoreOutlined,
  ClusterOutlined,
  CodeOutlined,
  FileTextOutlined,
  FolderOpenOutlined,
  SettingOutlined,
} from '@ant-design/icons';
import type { ComponentType } from 'react';
import { useEventStore } from '@/stores/events';
import { ThemeSwitch } from '@/components/ThemeSwitch';
import { cx } from '@/lib/cx';
import styles from './Shell.module.css';

interface Section {
  to: string;
  labelKey: string;
  Icon: ComponentType;
  end?: boolean;
}

const SECTIONS: Section[] = [
  { to: '/', labelKey: 'nav.overview', Icon: AppstoreOutlined, end: true },
  { to: '/servers', labelKey: 'nav.servers', Icon: ClusterOutlined },
  { to: '/tools', labelKey: 'nav.tools', Icon: CodeOutlined },
  { to: '/resources', labelKey: 'nav.resources', Icon: FolderOpenOutlined },
  { to: '/logs', labelKey: 'nav.logs', Icon: FileTextOutlined },
];
const SETTINGS: Section = { to: '/settings', labelKey: 'nav.settings', Icon: SettingOutlined };

function NavItem({ section }: { section: Section }) {
  const { t } = useTranslation();
  const label = t(section.labelKey);
  return (
    <NavLink
      to={section.to}
      end={section.end ?? false}
      aria-label={label}
      className={({ isActive }) => cx(styles.item, isActive && styles.active)}
    >
      <span className={styles.itemIcon} aria-hidden="true">
        <section.Icon />
      </span>
      <span>{label}</span>
    </NavLink>
  );
}

function ConnectionIndicator() {
  const { t } = useTranslation();
  const connection = useEventStore((s) => s.connection);
  return (
    <Tooltip title={t('connection.hint')}>
      <span className={styles.connection} role="status">
        <span className={cx(styles.dot, styles[connection])} aria-hidden="true" />
        {t(`connection.${connection}`)}
      </span>
    </Tooltip>
  );
}

export function Shell() {
  const { t } = useTranslation();
  const { pathname } = useLocation();
  const section = [...SECTIONS, SETTINGS].find((s) =>
    s.end ? pathname === s.to : pathname === s.to || pathname.startsWith(`${s.to}/`),
  );

  return (
    <div className={styles.shell}>
      <a href="#main-content" className={styles.skip}>
        {t('app.skipContent')}
      </a>
      <aside className={styles.sidebar}>
        <NavLink to="/" className={cx(styles.brand)} aria-label={t('app.name')}>
          <span className={styles.symbol} aria-hidden="true">
            <svg width="22" height="22" viewBox="0 0 24 24" fill="none">
              <path
                d="M4 6h5l6 6h5M4 18h5l6-6M4 12h5"
                stroke="currentColor"
                strokeWidth="1.7"
                strokeLinecap="round"
                strokeLinejoin="round"
              />
              <circle cx="4" cy="6" r="1.5" fill="currentColor" />
              <circle cx="4" cy="18" r="1.5" fill="currentColor" />
              <circle cx="20" cy="12" r="1.5" fill="currentColor" />
            </svg>
          </span>
          <span className={styles.wordmark}>
            mcp-foyer<span>{t('app.tagline')}</span>
          </span>
        </NavLink>

        <nav className={styles.navigation} aria-label={t('app.tagline')}>
          <span className={styles.groupLabel}>{t('app.workspace')}</span>
          {SECTIONS.map((item) => (
            <NavItem key={item.to} section={item} />
          ))}
          <span className={styles.spacer} />
          <span className={styles.groupLabel}>{t('app.manage')}</span>
          <NavItem section={SETTINGS} />
        </nav>

        <div className={styles.instance}>
          <span className={styles.instanceLabel}>{t('app.instance')}</span>
          <span className={styles.host} title={window.location.host}>
            {window.location.host}
          </span>
        </div>
      </aside>

      <div className={styles.main}>
        <header className={styles.topbar}>
          <span className={styles.crumb}>{t('app.workspace')}</span>
          <span className={styles.separator} aria-hidden="true">
            /
          </span>
          <span className={styles.section}>{section ? t(section.labelKey) : '404'}</span>
          <div className={styles.topbarRight}>
            <ConnectionIndicator />
            <span className={styles.divider} aria-hidden="true" />
            <ThemeSwitch />
          </div>
        </header>
        <main id="main-content" tabIndex={-1} className={styles.content}>
          <Outlet />
        </main>
      </div>
    </div>
  );
}
