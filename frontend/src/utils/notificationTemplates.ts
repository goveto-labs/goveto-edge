// Preset templates for composing Shoutrrr service URLs in the notification
// channel dialog. Each template id matches the service identifier the backend
// derives from the URL scheme (see internal/httpapi/clusters/notifications.go).
export interface NotificationTemplateField {
    key: string;
    label: string;
    placeholder?: string;
    secret?: boolean;
    optional?: boolean;
}

export interface NotificationTemplate {
    id: string;
    label: string;
    docsURL: string;
    fields: NotificationTemplateField[];
    build: (values: Record<string, string>) => string;
}

type Values = Record<string, string>;

const fieldValue = (values: Values, key: string) => values[key]?.trim() ?? '';

const docs = (service: string) => `https://containrrr.dev/shoutrrr/v0.8/services/${service}/`;

export const notificationTemplates: NotificationTemplate[] = [
    {
        id: 'discord',
        label: 'Discord',
        docsURL: docs('discord'),
        fields: [
            { key: 'webhookId', label: 'Webhook ID', placeholder: '693853386302554172' },
            { key: 'token', label: 'Webhook token', secret: true },
        ],
        build: (values) =>
            `discord://${encodeURIComponent(fieldValue(values, 'token'))}@${encodeURIComponent(fieldValue(values, 'webhookId'))}`,
    },
    {
        id: 'slack',
        label: 'Slack',
        docsURL: docs('slack'),
        fields: [
            {
                key: 'webhookUrl',
                label: 'Webhook URL',
                placeholder: 'https://hooks.slack.com/services/T…/B…/…',
            },
        ],
        build: (values) => {
            const match = fieldValue(values, 'webhookUrl').match(
                /^https:\/\/hooks\.slack\.com\/services\/([^/]+)\/([^/]+)\/([^/]+)\/?$/
            );
            if (!match) return '';
            return `slack://hook:${encodeURIComponent(`${match[1]}-${match[2]}-${match[3]}`)}@webhook`;
        },
    },
    {
        id: 'telegram',
        label: 'Telegram',
        docsURL: docs('telegram'),
        fields: [
            {
                key: 'token',
                label: 'Bot token',
                placeholder: '110201543:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw',
                secret: true,
            },
            { key: 'chats', label: 'Chat ID or @channel', placeholder: '@alerts' },
        ],
        build: (values) =>
            `telegram://${encodeURIComponent(fieldValue(values, 'token'))}@telegram?chats=${encodeURIComponent(fieldValue(values, 'chats'))}`,
    },
    {
        id: 'smtp',
        label: 'Email (SMTP)',
        docsURL: docs('smtp'),
        fields: [
            { key: 'host', label: 'SMTP host', placeholder: 'smtp.example.com' },
            { key: 'port', label: 'Port', placeholder: '465', optional: true },
            { key: 'username', label: 'Username' },
            { key: 'password', label: 'Password', secret: true },
            { key: 'from', label: 'From address', placeholder: 'alerts@example.com' },
            { key: 'to', label: 'Recipients', placeholder: 'ops@example.com, dev@example.com' },
        ],
        build: (values) => {
            const port = fieldValue(values, 'port') || '25';
            return (
                `smtp://${encodeURIComponent(fieldValue(values, 'username'))}:${encodeURIComponent(fieldValue(values, 'password'))}` +
                `@${fieldValue(values, 'host')}:${encodeURIComponent(port)}` +
                `/?from=${encodeURIComponent(fieldValue(values, 'from'))}&to=${encodeURIComponent(fieldValue(values, 'to'))}`
            );
        },
    },
    {
        id: 'ntfy',
        label: 'Ntfy',
        docsURL: docs('ntfy'),
        fields: [
            { key: 'topic', label: 'Topic', placeholder: 'goveto-alerts' },
            { key: 'host', label: 'Server', placeholder: 'ntfy.sh', optional: true },
            { key: 'username', label: 'Username', optional: true },
            { key: 'password', label: 'Password', secret: true, optional: true },
        ],
        build: (values) => {
            const host = fieldValue(values, 'host') || 'ntfy.sh';
            const username = fieldValue(values, 'username');
            const auth = username
                ? `${encodeURIComponent(username)}:${encodeURIComponent(fieldValue(values, 'password'))}@`
                : '';
            return `ntfy://${auth}${host}/${encodeURIComponent(fieldValue(values, 'topic'))}`;
        },
    },
    {
        id: 'gotify',
        label: 'Gotify',
        docsURL: docs('gotify'),
        fields: [
            { key: 'host', label: 'Server', placeholder: 'gotify.example.com' },
            { key: 'token', label: 'App token', secret: true },
        ],
        build: (values) =>
            `gotify://${fieldValue(values, 'host')}/${encodeURIComponent(fieldValue(values, 'token'))}`,
    },
    {
        id: 'pushover',
        label: 'Pushover',
        docsURL: docs('pushover'),
        fields: [
            { key: 'userKey', label: 'User key', secret: true },
            { key: 'apiToken', label: 'API token', secret: true },
        ],
        build: (values) =>
            `pushover://shoutrrr:${encodeURIComponent(fieldValue(values, 'apiToken'))}@${encodeURIComponent(fieldValue(values, 'userKey'))}/`,
    },
    {
        id: 'bark',
        label: 'Bark',
        docsURL: docs('bark'),
        fields: [
            { key: 'deviceKey', label: 'Device key', secret: true },
            { key: 'host', label: 'Server', placeholder: 'api.day.app', optional: true },
        ],
        build: (values) => {
            const host = fieldValue(values, 'host') || 'api.day.app';
            return `bark://:${encodeURIComponent(fieldValue(values, 'deviceKey'))}@${host}`;
        },
    },
    {
        id: 'generic',
        label: 'Generic webhook',
        docsURL: docs('generic'),
        fields: [
            {
                key: 'webhookUrl',
                label: 'Webhook URL',
                placeholder: 'https://example.com/api/v1/alerts',
            },
        ],
        build: (values) => {
            const url = fieldValue(values, 'webhookUrl');
            if (!url) return '';
            return /^https?:\/\//.test(url) ? `generic+${url}` : `generic://${url}`;
        },
    },
];

export const customTemplateId = 'custom';

export function findNotificationTemplate(id: string) {
    return notificationTemplates.find((template) => template.id === id);
}

export function requiredFieldsFilled(template: NotificationTemplate, values: Values) {
    return template.fields
        .filter((field) => !field.optional)
        .every((field) => fieldValue(values, field.key) !== '');
}

export function anyFieldFilled(template: NotificationTemplate, values: Values) {
    return template.fields.some((field) => fieldValue(values, field.key) !== '');
}
