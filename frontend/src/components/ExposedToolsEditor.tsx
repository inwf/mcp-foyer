import { useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { Button, Checkbox, Skeleton, Tooltip } from 'antd';
import { endpoints } from '@/api/endpoints';
import { keys } from '@/api/query';
import { StringListEditor } from '@/components/KeyValueEditor';
import { cx } from '@/lib/cx';
import styles from './ExposedToolsEditor.module.css';

/**
 * Choosing which of a server's tools the gateway offers.
 *
 * Typing tool names by hand is how this started, and it is the wrong way
 * round: the names are known — the server has already told the gateway
 * what it offers — so the work is picking from a list, not remembering
 * and spelling. A typo in a typed name silently exposes nothing, and
 * nothing on screen says so.
 *
 * The list is only available once the server is connected, and a server
 * being added is not connected yet. So the typed list stays as the
 * fallback rather than being removed: it is the only thing that works
 * before the first connection, and it is what still holds a name for a
 * tool that has since disappeared.
 */

export function ExposedToolsEditor({
  value = [],
  onChange,
  /** Absent when the server is being added and has no name yet. */
  server,
  disabledTools = [],
}: {
  disabledTools?: string[];
  value?: string[];
  onChange?: (value: string[]) => void;
  server?: string | undefined;
}) {
  const { t } = useTranslation();

  const tools = useQuery({
    queryKey: keys.servers.tools(server ?? ''),
    queryFn: () => endpoints.serverTools(server ?? ''),
    enabled: Boolean(server),
    // A server that is not connected answers with a failure rather than
    // an empty list, and retrying it on a drawer that is open for a
    // minute would be a request every few seconds for no gain.
    retry: false,
  });

  const names = useMemo(() => (tools.data ?? []).map((tool) => tool.name), [tools.data]);

  // A name in the configuration that the server no longer offers still
  // has to be visible: silently dropping it from a checkbox list would
  // make it disappear on the next save.
  const missing = useMemo(
    () => value.filter((name) => !names.includes(name)),
    [value, names],
  );

  if (!server || tools.isError) {
    // Nothing to pick from — either the server has never connected, or it
    // is not connected now. Typing is the only thing left that works.
    return (
      <div className={styles.fallback}>
        <StringListEditor value={value} {...(onChange ? { onChange } : {})} placeholder="read" />
        <p className={styles.note}>
          {server ? t('form.exposedToolsOffline') : t('form.exposedToolsUnsaved')}
        </p>
      </div>
    );
  }

  if (tools.isPending) return <Skeleton active paragraph={{ rows: 2 }} title={false} />;

  if (names.length === 0) {
    return <p className={styles.note}>{t('form.exposedToolsNone')}</p>;
  }

  const available = names.filter((name) => !disabledTools.includes(name));
  const all = available.length > 0 && available.every((name) => value.includes(name)) && missing.length === 0;
  const some = value.length > 0 && !all;

  return (
    <div className={styles.editor}>
      <div className={styles.head}>
        <Checkbox
          checked={all}
          indeterminate={some}
          onChange={(e) => onChange?.(e.target.checked ? available : [])}
        >
          {t('form.exposedToolsAll')}
        </Checkbox>
        <span className={styles.count}>
          {value.length === 0
            ? t('form.exposedToolsEveryOne')
            : t('form.exposedToolsCount', { count: value.length, total: names.length })}
        </span>
      </div>

      <Checkbox.Group
        className={cx(styles.group)}
        value={value}
        onChange={(next) => onChange?.(next as string[])}
      >
        {(tools.data ?? []).map((tool) => (
          <Checkbox key={tool.name} value={tool.name} disabled={disabledTools.includes(tool.name)} className={cx(styles.item)}>
            <Tooltip title={tool.description}>
              <span className={styles.name}>{tool.name}</span>
            </Tooltip>
          </Checkbox>
        ))}
      </Checkbox.Group>

      {missing.length > 0 ? (
        <div className={styles.missing}>
          <p className={styles.note}>{t('form.exposedToolsMissing')}</p>
          <div className={styles.missingList}>
            {missing.map((name) => (
              <span key={name} className={styles.gone}>
                {name}
                <Button
                  type="text"
                  size="small"
                  onClick={() => onChange?.(value.filter((kept) => kept !== name))}
                >
                  ×
                </Button>
              </span>
            ))}
          </div>
        </div>
      ) : null}
    </div>
  );
}
