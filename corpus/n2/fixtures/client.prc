CREATE OR REPLACE PROCEDURE n2_client(p_id NUMBER) IS
  l_result NUMBER;
  l_id n2_accounts.account_id%TYPE;
  l_row n2_accounts%ROWTYPE;
  l_address n2_address;
BEGIN
  l_result := n2_ledger.amount(p_id);
  n2_ledger.credit(p_id, l_result);
  SELECT external_score INTO l_result FROM n2_external WHERE external_id = p_id;
  l_id := l_row.account_id;
  IF l_address.street IS NULL THEN l_result := 1; END IF;
  n2_ext.n2_remote.ping(p_id);
  n2_r.ping(p_id);
  DBMS_OUTPUT.PUT_LINE(l_result);
END;
/
