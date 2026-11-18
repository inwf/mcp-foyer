import { useTranslation } from 'react-i18next';
import { Tooltip } from 'antd';
import type { ServerState } from '@/api/types';
import { cx } from '@/lib/cx';
import styles from './StateBadge.module.css';

const CLASS: Record<ServerState, string | undefined> = {
  connected: styles.connected,
  sleeping: styles.disconnected,
  connecting: styles.connecting,
  failed: styles.failed,
  disconnected: styles.disconnected,
};

/**
 * A server's state, as a word and a coloured dot.
 *
 * The error is on the badge itself rather than in a column of its own:
 * a failure message is a sentence, and a column wide enough for one
 * would be mostly empty. Hovering the thing that says "failed" is where
 * someone looks for why.
 */
export function StateBadge({
  state,
  error,
  enabled = true,
}: {
  state: ServerState;
  error?: string | undefined;
  enabled?: boolean | undefined;
}) {
  const { t } = useTranslation();

  const label =
    !enabled && state === 'disconnected' ? t('servers.disabled') : t(`state.${state}`);

  const badge = (
    <span className={cx(styles.badge, CLASS[state], !enabled && styles.off)}>
      <span className={styles.dot} />
      {label}
    </span>
  );

  if (!error) return badge;
  return (
    <Tooltip title={error} styles={{ root: { maxWidth: 480 } }}>
      {badge}
    </Tooltip>
  );
}
