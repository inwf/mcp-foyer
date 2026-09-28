import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { App, Alert, Button, Drawer, Form, Input, Segmented, Switch } from 'antd';
import { TRANSPORTS, type FieldError, type MCPServer, type Transport } from '@/api/types';
import { ApiError } from '@/api/client';
import { useServerActions } from '@/hooks/use-server-actions';
import { KeyValueEditor, StringListEditor } from '@/components/KeyValueEditor';
import { readPastedServer, withServerDefaults } from '@/lib/server-json';
import { ExposedToolsEditor } from '@/components/ExposedToolsEditor';

/*
 * Adding and editing a server.
 *
 * The two transports differ in what they need, and showing every field
 * for both would present a stdio server with a URL box it must leave
 * empty — which the gateway would then reject. So the form shows what
 * applies:
 *
 *   stdio            a command, its arguments and its environment
 *   streamable-http  a URL, its headers and an optional proxy
 *
 * Switching transport keeps whatever was typed. Someone converting a
 * local server to a remote one should not have to retype its name,
 * description and timeout; and the fields that no longer apply
 * are dropped on submission rather than on switch, so switching back
 * restores them.
 */

const NAME_PATTERN = /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/;

export interface ServerFormValues {
  name: string;
  transport: Transport;
  enabled: boolean;
  description: string;
  timeout: string;
  command: string;
  args: string[];
  env: Record<string, string>;
  url: string;
  headers: Record<string, string>;
  proxy: string;
  exposedTools: string[];
}

function emptyValues(): ServerFormValues {
  return {
    name: '',
    transport: 'stdio',
    enabled: true,
    description: '',
    timeout: '1m0s',
    command: '',
    args: [],
    env: {},
    url: '',
    headers: {},
    proxy: '',
    exposedTools: [],
  };
}

function valuesFrom(name: string, server: MCPServer): ServerFormValues {
  return {
    name,
    transport: server.transport,
    enabled: server.enabled,
    description: server.description ?? '',
    timeout: server.timeout,
    command: server.command ?? '',
    args: server.args ?? [],
    env: server.env ?? {},
    url: server.url ?? '',
    headers: server.headers ?? {},
    proxy: server.proxy ?? '',
    exposedTools: server.exposedTools ?? [],
  };
}

/** Builds what the API takes, dropping everything the chosen transport
 *  does not use and everything left empty. */
export function serverFrom(values: ServerFormValues, original?: MCPServer): MCPServer {
  const spawns = values.transport === 'stdio';

  // Preserve configuration fields with no corresponding form input.
  const extras = { ...original };
  for (const field of FIELD_NAMES) {
    if (field !== 'name') delete extras[field];
  }
  if (!spawns) {
    delete extras.readyPatterns;
    delete extras.readyTimeout;
  }
  const server: MCPServer = {
    ...extras,
    transport: values.transport,
    enabled: values.enabled,
    timeout: values.timeout.trim(),
  };

  if (values.description.trim()) server.description = values.description.trim();
  if (values.exposedTools.length) server.exposedTools = values.exposedTools;

  if (spawns) {
    if (values.command.trim()) server.command = values.command.trim();
    if (values.args.length) server.args = values.args;
    if (Object.keys(values.env).length) server.env = values.env;
  }
  if (!spawns) {
    if (values.url.trim()) server.url = values.url.trim();
    if (Object.keys(values.headers).length) server.headers = values.headers;
    if (values.proxy.trim()) server.proxy = values.proxy.trim();
  }

  return server;
}

/** Turns the reader's failure into something on screen. A parse failure
 *  carries the parser's own message, which names the position. */
function jsonMessage(t: (key: string) => string, reason: string): string {
  switch (reason) {
    case 'jsonNotAnObject':
      return t('form.jsonNotAnObject');
    case 'jsonOneServer':
      return t('form.jsonOneServer');
    default:
      return `${t('call.invalidJson')} — ${reason}`;
  }
}

/** The form's own field names, for recognising which of the gateway's
 *  field paths correspond to an input on screen. */
const FIELD_NAMES = [
  'name',
  'transport',
  'enabled',
  'description',
  'timeout',
  'command',
  'args',
  'env',
  'url',
  'headers',
  'proxy',
  'exposedTools',
] as const satisfies ReadonlyArray<keyof ServerFormValues>;

type FieldName = (typeof FIELD_NAMES)[number];

function isFieldName(value: string): value is FieldName {
  return (FIELD_NAMES as ReadonlyArray<string>).includes(value);
}

/**
 * Turns the gateway's field paths into the form's field names, so that a
 * rejected save marks the input that caused it rather than only printing
 * a sentence at the top.
 *
 * A path that does not correspond to an input is dropped rather than
 * guessed at: the gateway validates the whole configuration, so it can
 * legitimately report a problem that has no field on this form. Those
 * still reach the user through the message at the top.
 */
function markFields(
  fields: FieldError[],
  name: string,
): Array<{ name: FieldName; errors: string[] }> {
  const prefix = `mcpServers.${name}.`;
  const marked: Array<{ name: FieldName; errors: string[] }> = [];

  for (const field of fields) {
    const path = field.field.startsWith(prefix)
      ? field.field.slice(prefix.length)
      : field.field;
    if (isFieldName(path)) marked.push({ name: path, errors: [field.message] });
  }
  return marked;
}

/**
 * Adding and editing a server.
 *
 * The drawer is the outer shell; everything with state lives in the body
 * below and is keyed on what is being edited. That is what resets the
 * form between uses — rather than an effect that pushes the new values
 * into a form that still holds the old ones, which has to run after the
 * first render and so shows the previous server for a frame.
 */
export function ServerForm({
  open,
  onClose,
  /** Absent when adding. */
  editing,
}: {
  open: boolean;
  onClose: () => void;
  editing?: { name: string; server: MCPServer } | undefined;
}) {
  const { t } = useTranslation();

  return (
    <Drawer
      open={open}
      onClose={onClose}
      size={620}
      title={editing ? `${t('servers.edit')} · ${editing.name}` : t('servers.add')}
      destroyOnHidden
    >
      <FormBody key={editing?.name ?? '#new'} onClose={onClose} editing={editing} />
    </Drawer>
  );
}

function FormBody({
  onClose,
  editing,
}: {
  onClose: () => void;
  editing?: { name: string; server: MCPServer } | undefined;
}) {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const { create, update } = useServerActions();

  const initial = editing ? valuesFrom(editing.name, editing.server) : emptyValues();

  const [form] = Form.useForm<ServerFormValues>();
  const [transport, setTransport] = useState<Transport>(initial.transport);
  const [original, setOriginal] = useState(editing?.server);
  const [failure, setFailure] = useState<string | null>(null);

  // The form and the JSON editor are two views of one server, not two
  // forms. Switching either way carries whatever is currently entered.
  const [mode, setMode] = useState<'form' | 'json'>('form');
  const [json, setJson] = useState('');
  const [jsonError, setJsonError] = useState<string | null>(null);

  const spawns = transport === 'stdio';

  const showJSON = () => {
    // getFieldsValue rather than validateFields: switching to the JSON
    // view is not submitting, and someone who reaches for it because the
    // form cannot express what they need should not first be made to
    // satisfy the form.
    setJson(JSON.stringify(serverFrom(form.getFieldsValue(), original), null, 2));
    setJsonError(null);
    setMode('json');
  };

  const showForm = () => {
    let read: { name?: string; server: MCPServer };
    try {
      read = readPastedServer(json);
    } catch (error) {
      // Refusing to switch rather than switching and silently dropping
      // the text: the text is the work, and the form cannot hold it.
      setJsonError(jsonMessage(t, (error as Error).message));
      return;
    }

    const name = editing ? editing.name : (read.name ?? form.getFieldValue('name') ?? '');
    form.setFieldsValue(valuesFrom(String(name), withServerDefaults(read.server)));
    setOriginal(read.server);
    setTransport(withServerDefaults(read.server).transport);
    setJsonError(null);
    setMode('form');
  };

  const submit = async () => {
    // A form that does not validate is an expected outcome, not a
    // failure: antd has already marked the offending inputs, and
    // letting the rejection escape would put an unhandled rejection in
    // the console for someone leaving a required box empty.
    let name: string;
    let server: MCPServer;

    if (mode === 'json') {
      let read: { name?: string; server: MCPServer };
      try {
        read = readPastedServer(json);
      } catch (error) {
        setJsonError(jsonMessage(t, (error as Error).message));
        return;
      }
      name = editing ? editing.name : (read.name ?? String(form.getFieldValue('name') ?? '')).trim();
      server = read.server;

      if (!name) {
        setJsonError(t('form.required'));
        return;
      }
    } else {
      let values: ServerFormValues;
      try {
        values = await form.validateFields();
      } catch {
        return;
      }
      name = values.name.trim();
      server = serverFrom(values, original);
    }

    setFailure(null);
    setJsonError(null);

    try {
      if (editing) {
        await update.mutateAsync({ name: editing.name, server });
      } else {
        await create.mutateAsync({ name, server });
      }
      void message.success(t('settings.saved'));
      onClose();
    } catch (error) {
      const failed = error instanceof ApiError ? error : null;
      if (failed?.fields.length && mode === 'form') {
        form.setFields(markFields(failed.fields, editing?.name ?? name));
      }
      setFailure(failed?.message ?? t('error.unknown'));
    }
  };

  const saving = create.isPending || update.isPending;

  return (
    <>
      {failure ? (
        <Alert
          type="error"
          showIcon
          message={failure}
          style={{ marginBottom: 'var(--space-4)' }}
        />
      ) : null}

      {/* Two ways of saying the same thing. The form is quicker for the
          usual case; the JSON view is what someone reaches for when they
          have a configuration in hand, or want to see exactly what will
          be sent. */}
      <Segmented
        value={mode}
        onChange={(next) => (next === 'json' ? showJSON() : showForm())}
        options={[
          { label: t('form.asForm'), value: 'form' },
          { label: t('form.asJson'), value: 'json' },
        ]}
        block
        style={{ marginBottom: 'var(--space-4)' }}
      />

      {/* The form stays mounted while the JSON view is showing, so that
          switching back is instant and nothing entered is lost. */}
      <div style={mode === 'json' ? { display: 'none' } : undefined}>
      <Form form={form} layout="vertical" initialValues={initial} requiredMark={false}>
        <Form.Item
          name="name"
          label={t('form.name')}
          extra={t('form.nameHint')}
          rules={[
            { required: true, message: t('form.required') },
            {
              pattern: NAME_PATTERN,
              // The gateway enforces this too; checking here is what
              // stops a round trip to be told about a typo.
              message: t('form.nameHint'),
            },
          ]}
        >
          {/* A rename would be a different server as far as the gateway
              is concerned, so editing addresses the existing one. */}
          <Input className="mono" disabled={Boolean(editing)} autoFocus={!editing} />
        </Form.Item>

        <Form.Item name="transport" label={t('form.transport')}>
          <Segmented
            options={TRANSPORTS.map((value) => ({ label: value, value }))}
            onChange={(value) => setTransport(value as Transport)}
            block
          />
        </Form.Item>

        {/* The two transports are exhaustive and mutually exclusive:
            one runs the server, the other dials it. */}
        {spawns ? (
          <>
            <Form.Item
              name="command"
              label={t('form.command')}
              rules={[{ required: true, message: t('form.required') }]}
            >
              <Input className="mono" placeholder="npx" />
            </Form.Item>

            <Form.Item name="args" label={t('form.args')}>
              <StringListEditor placeholder="-y" />
            </Form.Item>

            <Form.Item name="env" label={t('form.env')}>
              <KeyValueEditor secret keyPlaceholder="API_TOKEN" />
            </Form.Item>
          </>
        ) : (
          <>
            <Form.Item
              name="url"
              label={t('form.url')}
              rules={[{ required: true, message: t('form.required') }]}
            >
              <Input className="mono" placeholder="https://example.com/mcp" />
            </Form.Item>

            <Form.Item name="headers" label={t('form.headers')}>
              <KeyValueEditor secret keyPlaceholder="Authorization" />
            </Form.Item>

            <Form.Item name="proxy" label={t('form.proxy')}>
              <Input className="mono" placeholder="http://127.0.0.1:7890" />
            </Form.Item>
          </>
        )}

        <Form.Item
          name="timeout"
          label={t('form.timeout')}
          extra={t('form.timeoutHint')}
          rules={[{ required: true, message: t('form.required') }]}
        >
          <Input className="mono" placeholder="1m0s" style={{ maxWidth: 180 }} />
        </Form.Item>

        <Form.Item
          name="description"
          label={t('form.description')}
          extra={t('form.descriptionHint')}
        >
          <Input.TextArea rows={2} />
        </Form.Item>

        {/* Picking from what the server actually offers, rather than
            typing names: the names are already known, and a typo in a
            typed one silently exposes nothing. */}
        <Form.Item
          name="exposedTools"
          label={t('form.exposedTools')}
          extra={t('form.exposedToolsHint')}
        >
          <ExposedToolsEditor server={editing?.name} disabledTools={original?.disabledTools ?? []} />
        </Form.Item>

        <Form.Item name="enabled" label={t('form.enabled')} valuePropName="checked">
          <Switch />
        </Form.Item>
      </Form>
      </div>

      {mode === 'json' ? (
        <div>
          <span className="label">{t('form.json')}</span>
          <p
            style={{
              margin: 'var(--space-1) 0 var(--space-2)',
              color: 'var(--ink-muted)',
              fontSize: 'var(--text-sm)',
              lineHeight: 1.6,
            }}
          >
            {t('form.jsonHint')}
          </p>
          <textarea
            className="mono"
            value={json}
            spellCheck={false}
            onChange={(e) => {
              setJson(e.target.value);
              setJsonError(null);
            }}
            rows={18}
            style={{
              display: 'block',
              width: '100%',
              padding: 'var(--space-3)',
              border: '1px solid var(--line-strong)',
              borderRadius: 'var(--radius)',
              background: 'var(--sunken)',
              color: 'var(--ink)',
              fontSize: 'var(--text-sm)',
              lineHeight: 1.6,
              resize: 'vertical',
              tabSize: 2,
            }}
          />
          {jsonError ? (
            <span
              style={{
                display: 'block',
                marginTop: 'var(--space-2)',
                color: 'var(--danger)',
                fontSize: 'var(--text-xs)',
              }}
            >
              {jsonError}
            </span>
          ) : null}
        </div>
      ) : null}

      <div
        style={{
          display: 'flex',
          gap: 'var(--space-2)',
          justifyContent: 'flex-end',
          paddingTop: 'var(--space-4)',
          borderTop: '1px solid var(--line)',
        }}
      >
        <Button onClick={onClose}>{t('form.cancel')}</Button>
        <Button type="primary" loading={saving} onClick={() => void submit()}>
          {editing ? t('form.save') : t('form.create')}
        </Button>
      </div>
    </>
  );
}
