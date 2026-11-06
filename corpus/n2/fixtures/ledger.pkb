CREATE OR REPLACE PACKAGE BODY n2_ledger AS
  FUNCTION amount(p_id NUMBER) RETURN NUMBER IS
    l_balance NUMBER;
  BEGIN
    SELECT balance INTO l_balance FROM n2_accounts WHERE account_id = p_id;
    RETURN l_balance;
  END;
  FUNCTION amount(p_id VARCHAR2) RETURN NUMBER IS
  BEGIN
    RETURN amount(TO_NUMBER(p_id));
  END;
  PROCEDURE credit(p_id NUMBER, p_delta NUMBER) IS
    l_delta NUMBER := p_delta;
  BEGIN
    DECLARE
      l_delta NUMBER := 1;
    BEGIN
      l_delta := l_delta + 1;
    END;
    UPDATE n2_accounts SET balance = balance + l_delta WHERE account_id = p_id;
  END;
END;
/
