import { useMemo, useState, type ReactNode } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import {
  App,
  Alert,
  Button,
  Form,
  Input,
  InputNumber,
  Segmented,
  Skeleton,
  Switch,
  Tabs,
} from 'antd';
import { parse, stringify } from 'yaml';
import { endpoints } from '@/api/endpoints';
import { keys } from '@/api/query';
import { ApiError } from '@/api/client';
import { LOG_LEVELS, type Config, type FieldError } from '@/api/types';
import { PageHeading } from '@/components/PageHeading';
import { ErrorNotice } from '@/components/ErrorNotice';
import { StringListEditor } from '@/components/KeyValueEditor';
import { ExportConfig } from '@/components/ExportConfig';
import { ImportConfig } from '@/components/ImportConfig';
import { cx } from '@/lib/cx';
import styles from './Settings.module.css';

/*
 * The gateway's own configuration, two ways.
 *
 * The form covers the settings with a shape worth knowing about. The
 * raw editor covers everything, including the servers — which have
 * their own page, so they are not duplicated here.
 *
 * Both edit the same document, because the API speaks the file's shape:
 * what the form calls `security.allowedNetworks` is spelled exactly that
 * way in the YAML, and a duration is the same string in both.
 */

/** The fields the form knows about, flattened. The rest of the document
 *  is carried through untouched, so editing the port cannot drop a
 *  setting this form has never heard of. */
interface FormValues {
  host: string;
  port: number;
  level: (typeof LOG_LEVELS)[number];
  format: 'console' | 'json';
  maxAge: string;
  maxSizeMB: number;
  mcpWireDebug: boolean;
  apiDebug: boolean;
  gatewayDebug: boolean;
  showTraceContext: boolean;
  allowedNetworks: string[];
  allowedOrigins: string[];
  maxConnections: number;
  maxConcurrentRequests: number;
  connectionTimeout: string;
  idleConnectionTimeout: string;
  defaultSessionMode: 'stateful' | 'stateless';
  /** User-Agent keywords selecting a mode, one list per mode. Flattened
   *  out of `gateway.sessionModeRules` because a form field holds one
   *  value, and these are two independent lists. */
  statefulClients: string[];
  statelessClients: string[];
  sessionTimeout: string;
  notifyDebounce: string;
  keepAlive: string;
  keepAliveFailureThreshold: number;
  connectDelay: string;
  maxRetries: number;
  retryBackoff: string;
  embeddingEnabled: boolean;
  embeddingURL: string;
  /** As the server handed it out: the placeholder when a key is stored,
   *  an empty string when there is none, or whatever was just typed. */
  embeddingApiKey: string;
  embeddingModel: string;
}

function valuesFrom(config: Config): FormValues {
  return {
    host: config.listen.host,
    port: config.listen.port,
    level: config.logging.level,
    format: config.logging.format,
    maxAge: config.logging.maxAge,
    maxSizeMB: config.logging.maxSizeMB,
    mcpWireDebug: config.logging.mcpWireDebug,
    apiDebug: config.logging.apiDebug,
    gatewayDebug: config.logging.gatewayDebug,
    showTraceContext: config.logging.showTraceContext,
    allowedNetworks: config.security.allowedNetworks,
    allowedOrigins: config.security.allowedOrigins ?? [],
    maxConnections: config.security.maxConnections,
    maxConcurrentRequests: config.security.maxConcurrentRequests,
    connectionTimeout: config.security.connectionTimeout,
    idleConnectionTimeout: config.security.idleConnectionTimeout,
    defaultSessionMode: config.gateway.defaultSessionMode,
    statefulClients: config.gateway.sessionModeRules.stateful ?? [],
    statelessClients: config.gateway.sessionModeRules.stateless ?? [],
    sessionTimeout: config.gateway.sessionTimeout,
    notifyDebounce: config.gateway.notifyDebounce,
    keepAlive: config.gateway.keepAlive,
    keepAliveFailureThreshold: config.gateway.keepAliveFailureThreshold,
    connectDelay: config.startup.connectDelay,
    maxRetries: config.startup.maxRetries,
    retryBackoff: config.startup.retryBackoff,
    embeddingEnabled: config.embedding.enabled,
    embeddingURL: config.embedding.url ?? '',
    embeddingApiKey: config.embedding.apiKey ?? '',
    embeddingModel: config.embedding.model ?? '',
  };
}

/** Folds the form back into the configuration it came from, leaving
 *  everything the form does not cover — the servers above all — exactly
 *  as it was. */
function applyTo(config: Config, values: FormValues): Config {
  return {
    ...config,
    listen: { host: values.host.trim(), port: values.port },
    logging: {
      ...config.logging,
      level: values.level,
      format: values.format,
      maxAge: values.maxAge.trim(),
      maxSizeMB: values.maxSizeMB,
      mcpWireDebug: values.mcpWireDebug,
      apiDebug: values.apiDebug,
      gatewayDebug: values.gatewayDebug,
      showTraceContext: values.showTraceContext,
    },
    security: {
      ...config.security,
      allowedNetworks: values.allowedNetworks,
      // An empty list here means "allow everyone", which is not the
      // same as the field being absent — so it is sent either way.
      ...(values.allowedOrigins.length ? { allowedOrigins: values.allowedOrigins } : {}),
      maxConnections: values.maxConnections,
      maxConcurrentRequests: values.maxConcurrentRequests,
      connectionTimeout: values.connectionTimeout.trim(),
      idleConnectionTimeout: values.idleConnectionTimeout.trim(),
    },
    gateway: {
      ...config.gateway,
      defaultSessionMode: values.defaultSessionMode,
      // Sent whether or not there is anything in them: an empty list and
      // an absent one mean the same thing here — no rules for that mode —
      // which is not true of `allowedNetworks` above, where empty means
      // "allow everyone". So there is nothing to preserve by omitting.
      sessionModeRules: {
        stateful: values.statefulClients,
        stateless: values.statelessClients,
      },
      sessionTimeout: values.sessionTimeout.trim(),
      notifyDebounce: values.notifyDebounce.trim(),
      keepAlive: values.keepAlive.trim(),
      keepAliveFailureThreshold: values.keepAliveFailureThreshold,
    },
    startup: {
      ...config.startup,
      connectDelay: values.connectDelay.trim(),
      maxRetries: values.maxRetries,
      retryBackoff: values.retryBackoff.trim(),
    },
    embedding: {
      ...config.embedding,
      enabled: values.embeddingEnabled,
      url: values.embeddingURL.trim(),
      // Sent back verbatim. The placeholder means "keep the stored key"
      // and the server restores it; an empty string clears the key; and
      // anything else is a key someone just typed.
      apiKey: values.embeddingApiKey.trim(),
      model: values.embeddingModel.trim(),
    },
  };
}

function FieldErrors({ fields }: { fields: FieldError[] }) {
  if (fields.length === 0) return null;
  return (
    <ul className={styles.fieldErrors}>
      {fields.map((field) => (
        <li key={field.field} className={styles.fieldError}>
          <span className={styles.fieldName}>{field.field}</span>
          <span className={styles.fieldMessage}>{field.message}</span>
        </li>
      ))}
    </ul>
  );
}

function SettingsSection({
  title,
  hint,
  children,
}: {
  title: string;
  hint: string;
  children: ReactNode;
}) {
  return (
    <section className={styles.formSection}>
      <header className={styles.sectionIntro}>
        <h2>{title}</h2>
        <p>{hint}</p>
      </header>
      <div className={styles.sectionBody}>{children}</div>
    </section>
  );
}

function SettingsForm({
  config,
  onSave,
  saving,
}: {
  config: Config;
  onSave: (next: Config) => void;
  saving: boolean;
}) {
  const { t } = useTranslation();
  const [form] = Form.useForm<FormValues>();
  const initial = useMemo(() => valuesFrom(config), [config]);

  return (
    <Form
      form={form}
      layout="vertical"
      initialValues={initial}
      requiredMark={false}
      onFinish={(values) => onSave(applyTo(config, values))}
    >
      <div className={styles.form}>
        <SettingsSection title={t('settings.listen')} hint={t('settings.listenHint')}>
          <div className={styles.grid}>
            <Form.Item name="host" label={t('settings.host')}>
              <Input className="mono" />
            </Form.Item>
            <Form.Item name="port" label={t('settings.port')} extra={t('settings.portHint')}>
              <InputNumber min={0} max={65535} className="mono" style={{ width: '100%' }} />
            </Form.Item>
          </div>
        </SettingsSection>

        <SettingsSection title={t('settings.logging')} hint={t('settings.loggingHint')}>
          <div className={styles.grid}>
            <Form.Item name="level" label={t('settings.level')}>
              <Segmented options={LOG_LEVELS.map((value) => ({ label: value, value }))} block />
            </Form.Item>
            <Form.Item name="format" label={t('settings.format')}>
              <Segmented options={['console', 'json']} block />
            </Form.Item>
            <Form.Item name="maxAge" label={t('settings.maxAge')} extra={t('form.timeoutHint')}>
              <Input className="mono" />
            </Form.Item>
            <Form.Item name="maxSizeMB" label={t('settings.maxSizeMB')}>
              <InputNumber min={1} className="mono" style={{ width: '100%' }} />
            </Form.Item>
            <Form.Item
              name="mcpWireDebug"
              label={t('settings.mcpWireDebug')}
              valuePropName="checked"
            >
              <Switch />
            </Form.Item>
            <Form.Item name="apiDebug" label={t('settings.apiDebug')} valuePropName="checked">
              <Switch />
            </Form.Item>
            <Form.Item
              name="gatewayDebug"
              label={t('settings.gatewayDebug')}
              extra={t('settings.gatewayDebugHint')}
              valuePropName="checked"
            >
              <Switch />
            </Form.Item>
            <Form.Item
              name="showTraceContext"
              label={t('settings.showTraceContext')}
              extra={t('settings.showTraceContextHint')}
              valuePropName="checked"
            >
              <Switch />
            </Form.Item>
          </div>
        </SettingsSection>

        <SettingsSection title={t('settings.security')} hint={t('settings.securityHint')}>
          <div className={styles.grid}>
            <Form.Item
              name="allowedNetworks"
              label={t('settings.allowedNetworks')}
              extra={t('settings.allowedNetworksHint')}
              className={cx(styles.wide)}
            >
              <StringListEditor placeholder="127.0.0.1/32" />
            </Form.Item>
            <Form.Item
              name="allowedOrigins"
              label={t('settings.allowedOrigins')}
              className={cx(styles.wide)}
            >
              <StringListEditor placeholder="http://localhost:5173" />
            </Form.Item>
            <Form.Item name="maxConnections" label={t('settings.maxConnections')}>
              <InputNumber min={1} className="mono" style={{ width: '100%' }} />
            </Form.Item>
            <Form.Item name="maxConcurrentRequests" label={t('settings.maxConcurrentRequests')}>
              <InputNumber min={1} className="mono" style={{ width: '100%' }} />
            </Form.Item>
            <Form.Item name="connectionTimeout" label={t('settings.connectionTimeout')}>
              <Input className="mono" />
            </Form.Item>
            <Form.Item name="idleConnectionTimeout" label={t('settings.idleConnectionTimeout')}>
              <Input className="mono" />
            </Form.Item>
          </div>
        </SettingsSection>

        <SettingsSection title={t('settings.gateway')} hint={t('settings.gatewayHint')}>
          <div className={styles.grid}>
            <Form.Item name="defaultSessionMode" label={t('settings.defaultSessionMode')}>
              <Segmented options={['stateful', 'stateless']} block />
            </Form.Item>
            <Form.Item name="sessionTimeout" label={t('settings.sessionTimeout')}>
              <Input className="mono" />
            </Form.Item>

            {/* The two keyword lists, side by side under one explanation.
                Read apart they look like two unrelated settings; what
                matters is that they are alternatives, and that the default
                above is what applies when neither matches. */}
            <p className={cx(styles.wide, styles.note)}>{t('settings.sessionModeRulesHint')}</p>
            <Form.Item
              name="statefulClients"
              label={t('settings.statefulClients')}
              className={cx(styles.wide)}
            >
              <StringListEditor placeholder="claude" />
            </Form.Item>
            <Form.Item
              name="statelessClients"
              label={t('settings.statelessClients')}
              className={cx(styles.wide)}
            >
              <StringListEditor placeholder="curl" />
            </Form.Item>

            <Form.Item name="notifyDebounce" label={t('settings.notifyDebounce')}>
              <Input className="mono" />
            </Form.Item>
            <Form.Item name="keepAlive" label={t('settings.keepAlive')}>
              <Input className="mono" />
            </Form.Item>
            <Form.Item
              name="keepAliveFailureThreshold"
              label={t('settings.keepAliveFailureThreshold')}
            >
              <InputNumber min={0} className="mono" style={{ width: '100%' }} />
            </Form.Item>
          </div>
        </SettingsSection>

        <SettingsSection title={t('settings.startup')} hint={t('settings.startupHint')}>
          <div className={styles.grid}>
            <Form.Item name="connectDelay" label={t('settings.connectDelay')}>
              <Input className="mono" />
            </Form.Item>
            <Form.Item name="maxRetries" label={t('settings.maxRetries')}>
              <InputNumber min={0} className="mono" style={{ width: '100%' }} />
            </Form.Item>
            <Form.Item name="retryBackoff" label={t('settings.retryBackoff')}>
              <Input className="mono" />
            </Form.Item>
          </div>
        </SettingsSection>

        <SettingsSection title={t('settings.embedding')} hint={t('settings.embeddingHint')}>
          <div className={styles.grid}>
            <Form.Item
              name="embeddingEnabled"
              label={t('settings.embeddingEnabled')}
              valuePropName="checked"
              className={cx(styles.wide)}
            >
              <Switch />
            </Form.Item>
            <Form.Item
              name="embeddingURL"
              label={t('settings.embeddingURL')}
              extra={t('settings.embeddingURLHint')}
              className={cx(styles.wide)}
            >
              <Input className="mono" placeholder="https://api.example.com/v1" />
            </Form.Item>
            <Form.Item
              name="embeddingModel"
              label={t('settings.embeddingModel')}
              extra={t('settings.embeddingModelHint')}
            >
              <Input className="mono" placeholder="bge-m3" />
            </Form.Item>
            <Form.Item
              name="embeddingApiKey"
              label={t('settings.embeddingApiKey')}
              extra={t('settings.embeddingApiKeyHint')}
            >
              {/* 不遮成密码框：占位符是有含义的文本，需要被看见。 */}
              <Input className="mono" autoComplete="off" />
            </Form.Item>
          </div>
        </SettingsSection>

        <div className={styles.actions}>
          <Button onClick={() => form.resetFields()}>{t('form.cancel')}</Button>
          <Button type="primary" htmlType="submit" loading={saving}>
            {t('form.save')}
          </Button>
        </div>
      </div>
    </Form>
  );
}

/**
 * The configuration as text.
 *
 * A plain textarea rather than a code editor: syntax highlighting for
 * YAML costs more in the bundle than the whole rest of this page, and
 * what actually catches mistakes here is the gateway's own validation,
 * which reports the offending field by name. That is wired to the
 * button, so an invalid document is refused before it is written.
 */
function RawEditor({
  config,
  onSave,
  saving,
}: {
  config: Config;
  onSave: (next: Config) => void;
  saving: boolean;
}) {
  const { t } = useTranslation();

  const original = useMemo(() => stringify(config, { indent: 2 }), [config]);
  const [text, setText] = useState(original);
  const [seed, setSeed] = useState(original);
  const [fields, setFields] = useState<FieldError[]>([]);

  // Re-seed when the configuration is reloaded from the gateway.
  if (seed !== original) {
    setSeed(original);
    setText(original);
    setFields([]);
  }

  const parsed = useMemo(() => {
    try {
      return { value: parse(text) as Config, error: null };
    } catch (error) {
      return { value: null, error: (error as Error).message };
    }
  }, [text]);

  const check = useMutation({
    mutationFn: (candidate: Config) => endpoints.validateConfig(candidate),
    onSuccess: (result, candidate) => {
      setFields(result.fields);
      if (result.valid) onSave(candidate);
    },
  });

  const dirty = text !== original;

  return (
    <div style={{ display: 'grid', gap: 'var(--space-3)' }}>
      <textarea
        aria-label={t('settings.yaml')}
        className={styles.editor}
        value={text}
        spellCheck={false}
        onChange={(e) => setText(e.target.value)}
      />

      {parsed.error ? (
        <span className={styles.problem}>
          {t('settings.invalidYaml')} — {parsed.error}
        </span>
      ) : null}

      <FieldErrors fields={fields} />

      <div className={styles.actions}>
        {dirty ? <span className={styles.dirty}>{t('settings.unsaved')}</span> : null}
        <Button onClick={() => setText(original)} disabled={!dirty}>
          {t('form.cancel')}
        </Button>
        <Button
          type="primary"
          loading={saving || check.isPending}
          disabled={parsed.error !== null || !dirty}
          // Checked before it is written, so a document the gateway
          // would reject never reaches the file.
          onClick={() => parsed.value && check.mutate(parsed.value)}
        >
          {t('form.save')}
        </Button>
      </div>
    </div>
  );
}

export default function Settings() {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const queryClient = useQueryClient();
  const [failure, setFailure] = useState<{ message: string; fields: FieldError[] } | null>(
    null,
  );

  const config = useQuery({ queryKey: keys.config.current(), queryFn: endpoints.getConfig });

  const save = useMutation({
    mutationFn: (next: Config) => endpoints.putConfig(next),
    onSuccess: (result) => {
      setFailure(null);
      void message.success(t('settings.saved'));
      // The listen address only takes effect on the next start, and
      // saying so beats someone wondering why the port did not change.
      if (result.changes.some((change) => change.field.startsWith('listen.'))) {
        void message.info(t('settings.restartNeeded'));
      }
      void queryClient.invalidateQueries();
    },
    onError: (error) => {
      const failed = error instanceof ApiError ? error : null;
      setFailure({
        message: failed?.message ?? t('error.unknown'),
        fields: failed?.fields ?? [],
      });
    },
  });

  const heading = (
    <PageHeading title={t('settings.title')} description={t('settings.description')} />
  );
  if (config.isPending)
    return (
      <div className={styles.page}>
        {heading}
        <Skeleton active paragraph={{ rows: 8 }} />
      </div>
    );
  if (config.isError) {
    return (
      <div className={styles.page}>
        {heading}
        <ErrorNotice error={config.error} onRetry={() => void config.refetch()} />
      </div>
    );
  }

  return (
    <div className={styles.page}>
      {heading}
      <div className={styles.path}>
        <span className="label">{t('settings.path')}</span>
        <span className={styles.pathValue}>{config.data.path}</span>
        <span className={styles.pathSpacer} />
        <ImportConfig />
        <ExportConfig />
      </div>

      {failure ? (
        <Alert
          type="error"
          showIcon
          message={failure.message}
          description={<FieldErrors fields={failure.fields} />}
          closable
          onClose={() => setFailure(null)}
        />
      ) : null}

      <Tabs
        items={[
          {
            key: 'form',
            label: t('settings.form'),
            children: (
              <SettingsForm
                config={config.data.config}
                onSave={(next) => save.mutate(next)}
                saving={save.isPending}
              />
            ),
          },
          {
            key: 'yaml',
            label: t('settings.yaml'),
            children: (
              <RawEditor
                config={config.data.config}
                onSave={(next) => save.mutate(next)}
                saving={save.isPending}
              />
            ),
          },
        ]}
      />
    </div>
  );
}
