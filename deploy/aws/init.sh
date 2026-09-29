#!/usr/bin/env bash
# Provisiona SQS, SNS e IAM no MiniStack (D-02, messaging.md §2). Idempotente.
# Roda com a chave raiz do emulador ("test"). Grava as chaves dos usuários IAM
# em $CREDS_FILE (INI, um profile por usuário), reaproveitando as que ainda
# são válidas, para que reexecuções não invalidem réplicas já no ar.
set -euo pipefail

WAGER_QUEUE="${SQS_WAGER_QUEUE_NAME:-wager-transactions.fifo}"
DLQ="${SQS_WAGER_DLQ_NAME:-wager-transactions-dlq.fifo}"
TOPIC="${SNS_EVENTS_TOPIC_NAME:-wallet-events.fifo}"
AUDIT_QUEUE="${SQS_EVENTS_AUDIT_QUEUE_NAME:-wallet-events-audit.fifo}"
MAX_RECEIVE="${SQS_MAX_RECEIVE_COUNT:-10}"
POLICY_DIR="${POLICY_DIR:-/deploy/aws/policies}"
CREDS_FILE="${CREDS_FILE:-/aws-shared/credentials}"
USERS=(pda-wallet-service provider-a provider-b)

log() { echo "[aws-init] $*"; }
queue_arn() { aws sqs get-queue-attributes --queue-url "$1" --attribute-names QueueArn --query Attributes.QueueArn --output text; }

# render <template>: substitui os placeholders de ARN e remove quebras de linha.
render() {
  sed -e "s|\${WAGER_QUEUE_ARN}|$WAGER_ARN|g" -e "s|\${DLQ_ARN}|$DLQ_ARN|g" \
      -e "s|\${TOPIC_ARN}|$TOPIC_ARN|g" -e "s|\${AUDIT_QUEUE_ARN}|$AUDIT_ARN|g" "$1" | tr -d '\n'
}

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

log "queues"
DLQ_URL=$(aws sqs create-queue --queue-name "$DLQ" \
  --attributes FifoQueue=true,ContentBasedDeduplication=false,MessageRetentionPeriod=1209600 \
  --query QueueUrl --output text)
DLQ_ARN=$(queue_arn "$DLQ_URL")

cat >"$tmp/wager-attrs.json" <<JSON
{"FifoQueue":"true","ContentBasedDeduplication":"false","VisibilityTimeout":"30","ReceiveMessageWaitTimeSeconds":"20","MessageRetentionPeriod":"345600","RedrivePolicy":"{\"deadLetterTargetArn\":\"$DLQ_ARN\",\"maxReceiveCount\":\"$MAX_RECEIVE\"}"}
JSON
WAGER_URL=$(aws sqs create-queue --queue-name "$WAGER_QUEUE" --attributes "file://$tmp/wager-attrs.json" --query QueueUrl --output text)
WAGER_ARN=$(queue_arn "$WAGER_URL")

log "topic and audit subscription"
TOPIC_ARN=$(aws sns create-topic --name "$TOPIC" --attributes FifoTopic=true,ContentBasedDeduplication=false --query TopicArn --output text)
AUDIT_URL=$(aws sqs create-queue --queue-name "$AUDIT_QUEUE" \
  --attributes FifoQueue=true,ContentBasedDeduplication=false,MessageRetentionPeriod=345600 \
  --query QueueUrl --output text)
AUDIT_ARN=$(queue_arn "$AUDIT_URL")

policy=$(render "$POLICY_DIR/wallet-events-audit.json" | sed 's/"/\\"/g')
printf '{"Policy":"%s"}' "$policy" >"$tmp/audit-policy.json"
aws sqs set-queue-attributes --queue-url "$AUDIT_URL" --attributes "file://$tmp/audit-policy.json"

existing_sub=$(aws sns list-subscriptions-by-topic --topic-arn "$TOPIC_ARN" \
  --query "Subscriptions[?Endpoint=='$AUDIT_ARN'].SubscriptionArn" --output text)
if [ -z "$existing_sub" ] || [ "$existing_sub" = "None" ]; then
  aws sns subscribe --topic-arn "$TOPIC_ARN" --protocol sqs --notification-endpoint "$AUDIT_ARN" \
    --attributes RawMessageDelivery=true >/dev/null
fi

log "iam users and identity policies"
policy_for() { if [ "$1" = pda-wallet-service ]; then echo "$POLICY_DIR/pda-wallet-service.json"; else echo "$POLICY_DIR/provider.json"; fi; }
# creds_field <user> <field>: lê um campo do profile no arquivo atual (se existir).
creds_field() {
  [ -f "$CREDS_FILE" ] || return 0
  awk -v p="[$1]" -v k="$2" '$0==p{f=1;next} /^\[/{f=0} f && $1==k {print $3}' "$CREDS_FILE"
}

: >"$tmp/credentials"
for user in "${USERS[@]}"; do
  aws iam get-user --user-name "$user" >/dev/null 2>&1 || aws iam create-user --user-name "$user" >/dev/null
  render "$(policy_for "$user")" >"$tmp/$user-policy.json"
  aws iam put-user-policy --user-name "$user" --policy-name "$user-least-privilege" --policy-document "file://$tmp/$user-policy.json"

  key_id=$(creds_field "$user" aws_access_key_id)
  secret=$(creds_field "$user" aws_secret_access_key)
  # echo sem aspas colapsa os TABs do --output text em espaços.
  # shellcheck disable=SC2046
  live=$(echo $(aws iam list-access-keys --user-name "$user" --query 'AccessKeyMetadata[].AccessKeyId' --output text))
  if [ -z "$key_id" ] || [[ " $live " != *" $key_id "* ]]; then
    for old in $live; do [ "$old" = "None" ] || aws iam delete-access-key --user-name "$user" --access-key-id "$old"; done
    read -r key_id secret < <(aws iam create-access-key --user-name "$user" \
      --query 'AccessKey.[AccessKeyId,SecretAccessKey]' --output text)
    log "new access key for $user"
  else
    log "reusing access key for $user"
  fi
  printf '[%s]\naws_access_key_id = %s\naws_secret_access_key = %s\n\n' "$user" "$key_id" "$secret" >>"$tmp/credentials"
done

mkdir -p "$(dirname "$CREDS_FILE")"
chmod 0644 "$tmp/credentials"
cp "$tmp/credentials" "$CREDS_FILE.tmp"
mv "$CREDS_FILE.tmp" "$CREDS_FILE"
log "done: wager=$WAGER_ARN dlq=$DLQ_ARN topic=$TOPIC_ARN audit=$AUDIT_ARN"
