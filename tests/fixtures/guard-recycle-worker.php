<?php
require_once __DIR__ . '/lib/worker_base.php';

$opts = eregion_parse_args($argv);
eregion_serve($opts, function (array $req, array $opts, int $handled) {
    $resp = eregion_ok_response($req, 'guard-body gen=' . $opts['generation']);
    $resp['meta']['recycle'] = true;
    $resp['meta']['recycle_reason'] = 'guard_state_violation';
    return $resp;
});
