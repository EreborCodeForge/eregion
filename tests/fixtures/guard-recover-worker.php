<?php
require_once __DIR__ . '/lib/worker_base.php';

$opts = eregion_parse_args($argv);
eregion_serve($opts, function (array $req, array $opts, int $handled) {
    $gen = (int) $opts['generation'];
    $resp = eregion_ok_response($req, 'recover gen=' . $gen . ' pid=' . getmypid());
    if ($gen < 3) {
        $resp['meta']['recycle'] = true;
        $resp['meta']['recycle_reason'] = 'guard_state_violation';
    }
    return $resp;
});
