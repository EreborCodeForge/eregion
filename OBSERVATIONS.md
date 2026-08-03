Algumas observações sobre esse arquivo:

workers.handshake_timeout pode ficar dentro de workers, como acima, porque controla a inicialização do processo. Na spec anterior ele estava dentro de protocol; escolha um único local e mantenha-o estável.
protocol.transport e protocol.codec não aparecem porque serão fixos no v1: UDS + MessagePack.
memory_limit_mb será avaliado cooperativamente pelo MithrilPHP usando memory_get_usage(true).
queue.capacity representa apenas requests aguardando, não os quatro que já estão sendo processados.
Em desenvolvimento, logging.format pode ser text; em produção, json é mais adequado.
O caminho do worker_script deve apontar para o entrypoint real fornecido pelo pacote MithrilPHP.