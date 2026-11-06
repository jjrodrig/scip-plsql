CREATE OR REPLACE PROCEDURE n2_client(p_id NUMBER) IS
  l_result NUMBER;
BEGIN
  l_result := n2_ledger.amount(p_id);
  n2_ledger.credit(p_id, l_result);
  SELECT external_score INTO l_result FROM n2_external WHERE external_id = p_id;
  DBMS_OUTPUT.PUT_LINE(l_result);
END;
/
