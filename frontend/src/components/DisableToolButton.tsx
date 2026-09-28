import { useMutation, useQueryClient } from '@tanstack/react-query';
import { App } from 'antd';
import { StopOutlined, CheckCircleOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { endpoints } from '@/api/endpoints';
import { keys } from '@/api/query';
import type { ServerView } from '@/api/types';
import { withToolDisabled } from '@/lib/tool-state';
import { IconButton } from './IconButton';

/** Disabling also removes exposure in the same configuration write.
 *  Enabling leaves the tool available for discovery without exposing it. */
export function DisableToolButton({ server, tool, disabled }: {
  server: ServerView;
  tool: string;
  disabled: boolean;
}) {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const client = useQueryClient();
  const change = useMutation({
    mutationFn: () => endpoints.updateServer(
      server.name, withToolDisabled(server.config, tool, !disabled),
    ),
    onSuccess: async () => {
      await Promise.all([
        client.invalidateQueries({ queryKey: keys.servers.all }),
        client.invalidateQueries({ queryKey: keys.tools.all }),
        client.invalidateQueries({ queryKey: keys.gateway.all }),
      ]);
    },
    onError: (error: Error) => { void message.error(error.message); },
  });
  return <IconButton
    size="small"
    color={disabled ? 'green' : 'danger'}
    variant="text"
    label={`${t(disabled ? 'tools.enableTool' : 'tools.disableTool')} ${tool}`}
    icon={disabled ? <CheckCircleOutlined aria-hidden /> : <StopOutlined aria-hidden />}
    loading={change.isPending}
    onClick={() => change.mutate()}
  />;
}
